//go:build kind_e2e

package k8s_e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

const (
	learningDriverImage = "ko.local/mecatl-learning-driver:e2e"
	learnedSkillName    = "restart-safe-workflow"
	learnedSkillBody    = "E2E_SKILL_BODY: answer with the terminal marker E2E_SKILL_USED."
	learnedTerminalText = "E2E_SKILL_USED"
	attemptClaimTTL     = 2 * time.Minute
)

var reflectionJSON = fmt.Sprintf(`{"kind":"proposed","candidates":[{"kind":"procedure","name":%q,"title":"Restart-safe workflow","body":%q,"evidence":["m:0"]}]}`, learnedSkillName, learnedSkillBody)

func learningFailoverSpecs() {
	ginkgo.Describe("remote learning worker failover", func() {
		ginkgo.It("recovers one claimed attempt and serves the activated skill from a replacement pod",
			ginkgo.SpecTimeout(7*time.Minute), func(ctx ginkgo.SpecContext) {
				installLearningFixture()
				deferLearningDiagnostics()

				claimingPod := singleAgentPod()
				addr, stop := portForward(claimingPod)

				ginkgo.By("creating a session and submitting an explicit procedure-learning request")
				createCtx, cancelCreate := shortCtx(30 * time.Second)
				sessionID := createSessionOverHTTP(createCtx, addr)
				cancelCreate()
				runCtx, cancelRun := shortCtx(60 * time.Second)
				defer cancelRun()
				requestBody, _ := json.Marshal(map[string]string{"text": "Turn this workflow into a skill: retain the restart-safe procedure for future sessions."})
				req, _ := http.NewRequestWithContext(runCtx, http.MethodPost, fmt.Sprintf("http://%s/v1/sessions/%s/prompt", addr, sessionID), bytes.NewReader(requestBody))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "text/event-stream")
				resp, err := http.DefaultClient.Do(req)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(resp.StatusCode).To(gomega.Equal(http.StatusOK))
				defer func() { _ = resp.Body.Close() }()
				go func() {
					_, _ = io.Copy(io.Discard, resp.Body)
				}()

				ginkgo.By("proving exactly one remote attempt was admitted and is durably claimed")
				var claimed learningAttempt
				gomega.Eventually(func(g gomega.Gomega) {
					page := getAttemptPage(addr)
					g.Expect(page.Attempts).To(gomega.HaveLen(1))
					claimed = page.Attempts[0]
					g.Expect(claimed.State).To(gomega.Equal("running"))
					g.Expect(claimed.ClaimGeneration).To(gomega.BeNumerically(">", 0))
					g.Expect(claimed.ClaimExpiresAt.Seconds).To(gomega.BeNumerically(">", 0))
				}, 45*time.Second, time.Second).Should(gomega.Succeed())
				attemptID := claimed.ID

				ginkgo.By("publishing the recovery script before replacing the claiming pod")
				recoveryScript, err := buildRecoveryLearningMockScript(reflectionJSON, learnedSkillName, learnedTerminalText)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				applyLearningConfigMap(ginkgoSuiteCtx(), recoveryScript)

				ginkgo.By("reconfirming the original pod still owns the live claim")
				gomega.Eventually(func(g gomega.Gomega) {
					page := getAttemptPage(addr)
					g.Expect(page.Attempts).To(gomega.HaveLen(1))
					current := page.Attempts[0]
					g.Expect(current.ID).To(gomega.Equal(attemptID))
					g.Expect(current.State).To(gomega.Equal("running"))
					g.Expect(current.ClaimGeneration).To(gomega.Equal(claimed.ClaimGeneration))
					g.Expect(current.ClaimExpiresAt.Seconds).To(gomega.BeNumerically(">", 0))
					expiresAt := current.ClaimExpiresAt.time()
					g.Expect(expiresAt).To(gomega.BeTemporally(">", time.Now()))
				}, 5*time.Second, 250*time.Millisecond).Should(gomega.Succeed())

				ginkgo.By("force-replacing only the claiming mecak8s pod while its claim is live")
				stop()
				kubectlDeletePod(claimingPod, true)
				replacement := waitReplacementReady([]string{claimingPod})
				replacementAddr, stopReplacement := portForward(replacement)
				defer stopReplacement()

				ginkgo.By("waiting for the production two-minute claim to expire and the replacement worker to complete the same attempt")
				var terminal learningAttempt
				gomega.Eventually(func(g gomega.Gomega) {
					page := getAttemptPage(replacementAddr)
					g.Expect(page.Attempts).To(gomega.HaveLen(1), "replacement must not admit a duplicate attempt")
					terminal = page.Attempts[0]
					g.Expect(terminal.ID).To(gomega.Equal(attemptID))
					g.Expect(terminal.State).To(gomega.Equal("completed"))
					g.Expect(terminal.Outcome).To(gomega.Equal("succeeded"))
					g.Expect(terminal.ProposalID).NotTo(gomega.BeEmpty())
					g.Expect(terminal.SkillID).NotTo(gomega.BeEmpty())
				}, attemptClaimTTL+90*time.Second, time.Second).Should(gomega.Succeed())

				ginkgo.By("listing the one active learned skill linked to the terminal attempt")
				skills := getLearnedSkills(replacementAddr)
				gomega.Expect(skills.Skills).To(gomega.HaveLen(1))
				gomega.Expect(skills.Skills[0].ID).To(gomega.Equal(terminal.SkillID))
				gomega.Expect(skills.Skills[0].Name).To(gomega.Equal(learnedSkillName))
				gomega.Expect(skills.Skills[0].State).To(gomega.Equal("active"))

				ginkgo.By("creating another session and consuming the learned skill through the Skill tool")
				secondCtx, cancelSecond := shortCtx(30 * time.Second)
				secondSession := createSessionOverHTTP(secondCtx, replacementAddr)
				cancelSecond()
				skillCtx, cancelSkill := shortCtx(90 * time.Second)
				observed, err := runSkillPrompt(skillCtx, replacementAddr, secondSession)
				cancelSkill()
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(observed.ToolName).To(gomega.Equal("Skill"))
				gomega.Expect(observed.ToolArgs).To(gomega.ContainSubstring(learnedSkillName))
				gomega.Expect(observed.ToolResult).To(gomega.ContainSubstring(learnedSkillBody))
				gomega.Expect(observed.TerminalText).To(gomega.Equal(learnedTerminalText))
			})
	})
}

type learningAttempt struct {
	ID              string       `json:"id"`
	State           string       `json:"state"`
	Outcome         string       `json:"outcome"`
	ClaimGeneration uint64       `json:"claim_generation"`
	ClaimExpiresAt  protobufTime `json:"claim_expires_at"`
	ProposalID      string       `json:"proposal_id"`
	SkillID         string       `json:"skill_id"`
}

type protobufTime struct {
	Seconds int64 `json:"seconds"`
	Nanos   int32 `json:"nanos"`
}

func (t protobufTime) time() time.Time {
	return time.Unix(t.Seconds, int64(t.Nanos))
}

type attemptPage struct {
	Attempts []learningAttempt `json:"attempts"`
}

type learnedSkillPage struct {
	Skills []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		State string `json:"state"`
	} `json:"skills"`
}

func getAttemptPage(addr string) attemptPage {
	ginkgo.GinkgoHelper()
	var page attemptPage
	getLearningJSON(addr, "/v1/learning/attempts?limit=10", &page)
	return page
}

func getLearnedSkills(addr string) learnedSkillPage {
	ginkgo.GinkgoHelper()
	var page learnedSkillPage
	getLearningJSON(addr, "/v1/skills/learned?state=active&limit=10&project=%2Ftmp", &page)
	return page
}

func getLearningJSON(addr, path string, target any) {
	ginkgo.GinkgoHelper()
	ctx, cancel := shortCtx(10 * time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	resp, err := http.DefaultClient.Do(req)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	gomega.ExpectWithOffset(1, resp.StatusCode).To(gomega.Equal(http.StatusOK), "GET %s: %s", path, formatBody(body))
	gomega.ExpectWithOffset(1, json.Unmarshal(body, target)).To(gomega.Succeed(), "decode GET %s", path)
}

type skillRunObservation struct {
	ToolName, ToolArgs, ToolResult, TerminalText string
}

func runSkillPrompt(ctx context.Context, addr, sessionID string) (skillRunObservation, error) {
	requestBody, _ := json.Marshal(map[string]string{"text": "Use the Skill tool to activate restart-safe-workflow, then report its required terminal marker."})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://%s/v1/sessions/%s/prompt", addr, sessionID), bytes.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return skillRunObservation{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return skillRunObservation{}, fmt.Errorf("skill run returned HTTP %d", resp.StatusCode)
	}
	var observed skillRunObservation
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if !strings.HasPrefix(scanner.Text(), "data: ") {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event) != nil {
			continue
		}
		switch event["type"] {
		case "tool.call":
			if value, ok := event["tool_call"].(map[string]any); ok {
				observed.ToolName, _ = value["name"].(string)
				observed.ToolArgs, _ = value["args"].(string)
			}
		case "tool.result":
			if value, ok := event["tool_result"].(map[string]any); ok {
				observed.ToolResult, _ = value["content"].(string)
			}
		case "result":
			if value, ok := event["result"].(map[string]any); ok {
				observed.TerminalText, _ = value["text"].(string)
			}
		}
	}
	return observed, scanner.Err()
}

func installLearningFixture() {
	ginkgo.GinkgoHelper()
	ctx := ginkgoSuiteCtx()
	valuesPath := filepath.Join(repoRoot(), ".scratch", "k8s-learning-e2e-values.yaml")
	ginkgo.DeferCleanup(func() {
		defer func() { _ = os.Remove(valuesPath) }()
		ginkgo.By("restoring the baseline chart after the learning fixture")
		helmInstallMecak8sChart()
		runCmd(ginkgoSuiteCtx(), "kubectl", "delete", "-n", k8sNamespace,
			"deployment/learning-driver", "service/learning-driver",
			"secret/learning-driver-tls", "configmap/learning-e2e-config",
			"networkpolicy/mecak8s-agent-allow-learning-driver",
			"--ignore-not-found")
		refreshPods()
	})
	cert, key := generateLearningTLS()
	applyOpaqueSecret(ctx, "learning-driver-tls", map[string][]byte{"tls.crt": cert, "tls.key": key, "ca.crt": cert})
	initialScript, err := buildInitialLearningMockScript(reflectionJSON, learnedSkillName, learnedTerminalText)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	applyLearningConfigMap(ctx, initialScript)
	runCmd(ctx, "kubectl", "apply", "-n", k8sNamespace, "-f", filepath.Join(repoRoot(), "deploy", "mecak8s-kind", "learning-driver.yaml"))
	runCmd(ctx, "kubectl", "wait", "-n", k8sNamespace, "--for=condition=Available", "deployment/learning-driver", "--timeout=120s")
	kubectlApplyStdin(ctx, []byte(learningDriverEgressPolicy))

	gomega.Expect(os.MkdirAll(filepath.Dir(valuesPath), 0o700)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(valuesPath, []byte(learningValues), 0o600)).To(gomega.Succeed())
	chart := filepath.Join(repoRoot(), "deploy", "helm", "mecak8s")
	out, err := exec.CommandContext(ctx, "helm", "upgrade", "--install", "mecak8s", chart,
		"--namespace", k8sNamespace, "--values", filepath.Join(chart, "values-kind.yaml"), "--values", valuesPath,
		"--set", "image.repository=ko.local/mecak8s", "--set", "image.tag=e2e", "--set", "fullnameOverride=mecak8s-agent",
		"--force-conflicts", "--wait", "--timeout=4m").CombinedOutput()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "install learning fixture chart: %s", out)
	_ = singleAgentPod()
}

func singleAgentPod() string {
	ginkgo.GinkgoHelper()
	var names []string
	gomega.Eventually(func(g gomega.Gomega) {
		out := runCmdQuiet("kubectl", "get", "pods", "-n", k8sNamespace, "-l", "app.kubernetes.io/component="+agentComponent,
			"--field-selector=status.phase==Running", "-o", "jsonpath={.items[?(@.status.containerStatuses[0].ready==true)].metadata.name}")
		names = strings.Fields(out)
		g.Expect(names).To(gomega.HaveLen(1))
	}, 120*time.Second, 2*time.Second).Should(gomega.Succeed())
	return names[0]
}

func applyOpaqueSecret(ctx context.Context, name string, data map[string][]byte) {
	manifest := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]string{"name": name, "namespace": k8sNamespace}, "type": "Opaque", "data": map[string]string{}}
	encoded := manifest["data"].(map[string]string)
	for key, value := range data {
		encoded[key] = base64.StdEncoding.EncodeToString(value)
	}
	body, _ := json.Marshal(manifest)
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = bytes.NewReader(body)
	out, err := cmd.CombinedOutput()
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred(), "apply generated TLS Secret: %s", out)
}

func applyLearningConfigMap(ctx context.Context, script []byte) {
	ginkgo.GinkgoHelper()
	manifest := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]string{"name": "learning-e2e-config", "namespace": k8sNamespace}, "data": map[string]string{
		"settings.yaml":    "learning:\n  mode: auto\n  skills:\n    activation: validated\n",
		"mock-script.json": string(script),
	}}
	body, err := json.Marshal(manifest)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	kubectlApplyStdin(ctx, body)

	// Apply completing updates the API object before the claiming pod is deleted.
	// Verify that exact revision without printing configuration or scripted content;
	// the running process has already loaded its provider turns into memory, while a
	// replacement pod mounts the now-current ConfigMap.
	gomega.EventuallyWithOffset(1, func() bool {
		out, getErr := exec.CommandContext(ctx, "kubectl", "get", "configmap", "learning-e2e-config", "-n", k8sNamespace, "-o", "json").Output()
		if getErr != nil {
			return false
		}
		var current struct {
			Data map[string]string `json:"data"`
		}
		return json.Unmarshal(out, &current) == nil && current.Data["mock-script.json"] == string(script)
	}, 15*time.Second, 250*time.Millisecond).Should(gomega.BeTrue(), "learning ConfigMap did not reach the requested script revision")
}

func generateLearningTLS() (certPEM, keyPEM []byte) {
	ginkgo.GinkgoHelper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	now := time.Now().UTC()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "learning-driver"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"learning-driver", "learning-driver.mecatl.svc", "learning-driver.mecatl.svc.cluster.local"},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	gomega.ExpectWithOffset(1, err).NotTo(gomega.HaveOccurred())
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
}

func deferLearningDiagnostics() {
	ginkgo.DeferCleanup(func() {
		if !ginkgo.CurrentSpecReport().Failed() {
			return
		}
		// Deliberately content-free: pod phase/restarts and attempt lifecycle only.
		ginkgo.GinkgoWriter.Printf("learning fixture pod status:\n%s\n", runCmdQuiet("kubectl", "get", "pods", "-n", k8sNamespace,
			"-l", "app.kubernetes.io/component=agent", "-o", "custom-columns=NAME:.metadata.name,PHASE:.status.phase,READY:.status.containerStatuses[0].ready,RESTARTS:.status.containerStatuses[0].restartCount"))
	})
}

const learningValues = `
replicaCount: 1
mockProvider: true
learning:
  store:
    endpoint: learning-driver:8443
    tls:
      enabled: true
      caSecret: learning-driver-tls
      caKey: ca.crt
extraEnv:
  - name: XDG_CONFIG_HOME
    value: /var/run/mecatl-e2e
extraArgs:
  - --mock-script=/var/run/mecatl-e2e/mock-script.json
  - --user-model-dir=/tmp/learning-user-model
  - --trust-project
extraVolumeMounts:
  - name: learning-e2e-config
    mountPath: /var/run/mecatl-e2e
    readOnly: true
extraVolumes:
  - name: learning-e2e-config
    configMap:
      name: learning-e2e-config
      items:
        - {key: settings.yaml, path: mecatl/settings.yaml}
        - {key: mock-script.json, path: mock-script.json}
`

const learningDriverEgressPolicy = `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: mecak8s-agent-allow-learning-driver, namespace: mecatl}
spec:
  podSelector:
    matchLabels: {app.kubernetes.io/name: mecak8s, app.kubernetes.io/component: agent}
  policyTypes: [Egress]
  egress:
    - to:
        - podSelector:
            matchLabels: {app.kubernetes.io/name: learning-driver}
      ports: [{protocol: TCP, port: 8443}]
`
