package executionkind_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestFailureEvidenceRejectsProducerText(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	// Even a syntactically valid metadata name or reason is not an output grant.
	const hostile = "PRIVATE-URL-TOKEN-COMMAND"
	fixture := `{"items":[{"kind":"ExecutionEnvironment","metadata":{"name":"` + hostile + `","deletionTimestamp":"private","finalizers":["` + hostile + `","execution.mecatl.dev/retain-workspace"]},"status":{"pod":{"name":"p","uid":"expected"},"pvc":{"name":"v","uid":"expected"},"activeOperation":{"id":"` + hostile + `","expiresAt":"2000-01-01T00:00:00.123Z"},"lifecycleOperation":{"type":"DeleteRetiredEnvironment","phase":"ReleasingSlot"},"conditions":[{"type":"Ready","status":"False","reason":"FenceUnknown","message":"` + hostile + `"}]}},{"kind":"Pod","metadata":{"name":"p","uid":"foreign","finalizers":["execution.mecatl.dev/verify-termination"]},"status":{"phase":"Failed","containerStatuses":[{"name":"` + hostile + `","state":{"terminated":{"reason":"Error","message":"` + hostile + `"}}}]}},{"kind":"PersistentVolumeClaim","metadata":{"name":"v","uid":"expected","finalizers":["kubernetes.io/pvc-protection"]}},{"kind":"ResourceQuota","spec":{"hard":{"pods":"10","requests.cpu":"10","` + hostile + `":"10"}},"status":{"hard":{"pods":"10","requests.cpu":"20"},"used":{"requests.cpu":"0"}}},{"kind":"Event","reason":"` + hostile + `","message":"` + hostile + `"}]}`
	cmd := exec.CommandContext(t.Context(), "jq", "-cs", "-f", "failure-evidence.jq")
	cmd.Stdin = strings.NewReader(fixture)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("filter: %v: %s", err, out)
	}
	if strings.Contains(string(out), hostile) || len(out) > 16384 {
		t.Fatal("untrusted evidence escaped")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatal(err)
		}
		if v["kind"] == "environment" {
			if v["pod_uid_matches"] != false || v["pvc_uid_matches"] != true || v["lease_expired"] != true || v["deleting"] != true || len(v["finalizers"].([]any)) != 1 {
				t.Fatalf("lost diagnostic facts: %s", line)
			}
		}
		if v["kind"] == "quota" && len(v["missing_or_mismatched_keys"].([]any)) != 2 {
			t.Fatalf("lost quota mismatch: %s", line)
		}
	}
}

func TestCreateStageProjectionRejectsRawAndPrivateData(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	const sentinel = "PRIVATE-URL-TOKEN-OWNER-REF-STACK"
	valid := `{"msg":"remote create stage","level":"DEBUG","stage":"attach_poll","reason":"not_ready_nonretryable","elapsed_ms":480000,"calls":4800,"session":"0123456789abcdef0123456789abcdef","error":"` + sentinel + `","grant":"` + sentinel + `"}`
	backend := `{"msg":"execution backend failure","level":"WARN","operation":"delete_retired","reason":"invalid","error":"` + sentinel + `","resource":"` + sentinel + `"}`
	ensure := `{"msg":"execution backend failure","level":"WARN","operation":"ensure_template","reason":"conflict","error":"` + sentinel + `","resource":"` + sentinel + `"}`
	input := sentinel + "\n" + `{"msg":"` + sentinel + `"}` + "\n" + valid + "\n" + backend + "\n" + ensure + "\n" + strings.ReplaceAll(ensure, `"reason":"conflict"`, `"reason":"`+sentinel+`"`) + "\n" + strings.ReplaceAll(ensure, `"operation":"ensure_template"`, `"operation":"`+sentinel+`"`) + "\n"
	for _, field := range []string{"stage", "reason", "session", "elapsed_ms", "calls"} {
		var row map[string]any
		if err := json.Unmarshal([]byte(valid), &row); err != nil {
			t.Fatal(err)
		}
		row[field] = sentinel
		bad, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		input += string(bad) + "\n"
	}
	cmd := exec.CommandContext(t.Context(), "jq", "-Rnc", "--arg", "source", "mecak8s", "-f", "create-stages.jq")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("projection failed: %v", err)
	}
	if strings.Contains(string(out), sentinel) || strings.Contains(string(out), "grant") || strings.Count(string(out), `"kind":"create_stage"`) != 1 || strings.Count(string(out), `"kind":"backend_failure"`) != 2 || !strings.Contains(string(out), `"operation":"ensure_template","reason":"conflict"`) || !strings.Contains(string(out), `"operation":"delete_retired","reason":"invalid"`) || !strings.Contains(string(out), "not_ready_nonretryable") {
		t.Fatal("projection leaked or lost discriminating state")
	}
	cmd = exec.CommandContext(t.Context(), "jq", "-Rnc", "--arg", "source", "mecak8s", "-f", "create-stages.jq")
	churn := valid + "\n" + strings.ReplaceAll(valid, "not_ready_nonretryable", "not_ready_retryable") + "\n"
	terminal := strings.ReplaceAll(valid, "not_ready_nonretryable", "error")
	input = strings.Repeat(churn, 750) + strings.ReplaceAll(terminal, "attach_poll", "attach_poll_end") + "\n" + strings.ReplaceAll(terminal, "attach_poll", "engine_factory") + "\n"
	input += sentinel + "\n" + strings.ReplaceAll(valid, "attach_poll", sentinel) + "\n"
	cmd.Stdin = strings.NewReader(input)
	out, err = cmd.CombinedOutput()
	if err != nil || strings.Count(string(out), `"kind":"create_stage"`) != 512 || len(out) > 262144 {
		t.Fatal("projection did not drain and bound the input")
	}
	if !strings.Contains(string(out), `"stage":"attach_poll_end","reason":"error"`) || !strings.Contains(string(out), `"stage":"engine_factory","reason":"error"`) {
		t.Fatal("projection lost latest terminal stages")
	}
	if strings.Contains(string(out), sentinel) || strings.Contains(string(out), "grant") {
		t.Fatal("bounded projection leaked raw or private data")
	}
}

func TestFailureCollectorRetainsLateEvidenceWithinCallerBudgets(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	collector, err := os.ReadFile("collect-failure.sh")
	if err != nil {
		t.Fatal(err)
	}
	requests := regexp.MustCompile(`--request-timeout=(\d+)s`).FindAllStringSubmatch(string(collector), -1)
	if len(requests) != 3 {
		t.Fatal("expected bounded list, log and resource requests")
	}
	perRequest, err := strconv.Atoi(requests[0][1])
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range requests[1:] {
		if request[1] != requests[0][1] {
			t.Fatal("collector requests have different deadlines")
		}
	}
	// Two lists, four pod logs and five resources can each consume their full deadline.
	for _, caller := range []string{"live.sh", "run.sh"} {
		content, err := os.ReadFile(caller)
		if err != nil {
			t.Fatal(err)
		}
		outer := regexp.MustCompile(`timeout --kill-after=5s (\d+)s "\$@"`).FindStringSubmatch(string(content))
		if outer == nil {
			t.Fatalf("%s collector deadline missing", caller)
		}
		budget, err := strconv.Atoi(outer[1])
		if err != nil || budget < 11*perRequest+25 {
			t.Fatalf("%s deadline %s does not cover eleven requests plus projection margin", caller, outer[1])
		}
	}

	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	clock := filepath.Join(root, "clock")
	writeFixture(t, clock, "0\n", 0o600)
	writeFixture(t, filepath.Join(bin, "kubectl"), `#!/bin/sh
set -eu
test "$1 $2 $3 $4 $5" = '--kubeconfig synthetic --context kind-owned --request-timeout=5s'
read -r tick < "$CLOCK"
tick=$((tick + 1))
printf '%s\n' "$tick" > "$CLOCK"
case "$6 $7" in
  'get pods')
    if [ "${10:-}" = -l ]; then
      printf '{"items":[{"metadata":{"name":"pod-a"}},{"metadata":{"name":"pod-b"}}]}'
      exit
    fi ;;
  'logs pod/pod-a'|'logs pod/pod-b') exit ;;
esac
case "$7" in
  resourcequotas) printf '{"items":[{"kind":"ResourceQuota","spec":{"hard":{"pods":"1"}}}]}' ;;
  events) test "$tick" -eq 11; printf '{"items":[{"kind":"Event","reason":"FailedScheduling"}]}' ;;
  *) printf '{"items":[]}' ;;
esac
`, 0o700)
	output := filepath.Join(root, "evidence")
	script, err := filepath.Abs("collect-failure.sh")
	if err != nil {
		t.Fatal(err)
	}
	out, err := runStep(t, root, "sh \"$COLLECTOR\" synthetic kind-owned \"$OUT\"", "COLLECTOR="+script, "OUT="+output, "CLOCK="+clock, "PATH="+bin+":"+os.Getenv("PATH"))
	if err != nil {
		t.Fatalf("collector: %v: %s", err, out)
	}
	data, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(data), `"kind":"quota"`) || !strings.Contains(string(data), `"kind":"event","reason":"FailedScheduling"`) {
		t.Fatalf("late quota/event evidence missing: %v: %s", err, data)
	}
	ticks, err := os.ReadFile(clock)
	if err != nil || string(ticks) != "11\n" {
		t.Fatalf("expected eleven sequential requests, got %q: %v", ticks, err)
	}
}

func TestFailureCollectorBoundsAndContext(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq unavailable")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(bin, "kubectl"), `#!/bin/sh
set -eu
test "$1 $2 $3 $4 $5" = '--kubeconfig synthetic --context kind-owned --request-timeout=5s'
if [ "$6" = logs ]; then
  test "${8} ${9} ${10} ${11} ${12}" = '-n execution-qualification --all-containers=true --tail=1000 --limit-bytes=262144'
  case "$7" in pod/mecak8s-1|pod/provider-1|pod/provider-2) ;; *) exit 99 ;; esac
  printf 'PRIVATE-RAW-STACK\n{"msg":"remote create stage","level":"DEBUG","stage":"bind_ensure","reason":"begin","calls":0,"elapsed_ms":1,"session":"","error":"PRIVATE-ERROR"}\n'
  if [ "$7" = pod/provider-2 ]; then printf '{"msg":"execution backend failure","level":"WARN","operation":"ensure_template","reason":"invalid","error":"PRIVATE-ERROR"}\n'; fi
  if [ "${LOG_FAILURE:-}" = 1 ]; then printf 'PRIVATE-LOG-ERROR' >&2; exit 17; fi
  exit 0
fi
if [ "$6" = get ] && [ "$7" = pods ] && [ "$8" = -n ] && [ "${10:-}" = -l ]; then
  test "$9" = execution-qualification && test "${10}" = -l && test "${12} ${13}" = '-o json'
  case "${11}" in
    app.kubernetes.io/name=mecak8s) printf '{"items":[{"metadata":{"name":"mecak8s-1"}}]}' ;;
    app.kubernetes.io/name=mecatl-execution) printf '{"items":[{"metadata":{"name":"provider-2"}},{"metadata":{"name":"PRIVATE-CREDENTIAL;touch-danger"}},{"metadata":{"name":"provider-1"}}]}' ;;
    *) exit 99 ;;
  esac
  exit 0
fi
test "$6 ${8} ${9} ${10} ${11}" = 'get -n execution-qualification -o json'
if [ -n "${EVIDENCE_FIXTURE:-}" ]; then cat "$EVIDENCE_FIXTURE"; exit; fi
case "$7" in
pods) printf 'PRIVATE-ERROR' >&2; exit 1 ;;
*) printf '{"items":[{"kind":"Event","reason":"FailedMount","message":"PRIVATE-DATA"}]}' ;;
esac
`, 0o700)
	outPath := filepath.Join(root, "evidence")
	script, err := filepath.Abs("collect-failure.sh")
	if err != nil {
		t.Fatal(err)
	}
	out, err := runStep(t, root, "sh \"$COLLECTOR\" synthetic kind-owned \"$OUT\"", "COLLECTOR="+script, "OUT="+outPath, "PATH="+bin+":"+os.Getenv("PATH"))
	if err == nil {
		t.Fatal("collector hid failed resource API")
	}
	data, err := os.ReadFile(outPath)
	if err != nil || len(data) > 16384 || len(data) == 0 || strings.Contains(string(data), "PRIVATE") || strings.Contains(string(out), "PRIVATE") {
		t.Fatalf("unsafe/missing evidence: %v", err)
	}
	if !strings.Contains(string(data), `"unavailable":1`) || strings.Count(string(data), `"kind":"create_stage"`) != 3 || !strings.Contains(string(data), `"operation":"ensure_template","reason":"invalid"`) {
		t.Fatal("API failure or projected workload stages not recorded")
	}
	if _, err := runStep(t, root, "sh \"$COLLECTOR\" synthetic kind-owned \"$OUT\"", "COLLECTOR="+script, "OUT="+outPath, "PATH="+bin+":"+os.Getenv("PATH")); err == nil {
		t.Fatal("collector overwrote earlier real-process evidence")
	}
	preserved, err := os.ReadFile(outPath)
	if err != nil || string(preserved) != string(data) {
		t.Fatal("earlier evidence changed")
	}
	// A large fleet must retain late quota evidence, not exhaust a summary-sized
	// cap on environment/Pod rows before reaching the quota and event sections.
	container := `{"state":{"terminated":{"reason":"Completed"}}}`
	pod := `{"kind":"Pod","status":{"phase":"Failed","containerStatuses":[` + strings.TrimSuffix(strings.Repeat(container+",", 16), ",") + `]}}`
	fixture := filepath.Join(root, "fleet.json")
	writeFixture(t, fixture, `{"items":[`+strings.Repeat(pod+",", 64)+`{"kind":"ResourceQuota","spec":{"hard":{"pods":"1"}}}]}`, 0o600)
	outPath = filepath.Join(root, "fleet-evidence")
	out, err = runStep(t, root, "sh \"$COLLECTOR\" synthetic kind-owned \"$OUT\"", "COLLECTOR="+script, "OUT="+outPath, "EVIDENCE_FIXTURE="+fixture, "PATH="+bin+":"+os.Getenv("PATH"))
	if err != nil {
		t.Fatalf("fleet collector: %v: %s", err, out)
	}
	data, err = os.ReadFile(outPath)
	if err != nil || len(data) > 1<<20 || !strings.Contains(string(data), `"kind":"quota"`) {
		t.Fatal("fleet evidence lost quota or exceeded artifact cap")
	}
	outPath = filepath.Join(root, "log-failure-evidence")
	out, err = runStep(t, root, "sh \"$COLLECTOR\" synthetic kind-owned \"$OUT\"", "COLLECTOR="+script, "OUT="+outPath, "EVIDENCE_FIXTURE="+fixture, "LOG_FAILURE=1", "PATH="+bin+":"+os.Getenv("PATH"))
	if err == nil || strings.Contains(string(out), "PRIVATE") {
		t.Fatal("log API failure hidden or leaked")
	}
	data, err = os.ReadFile(outPath)
	if err != nil || strings.Contains(string(data), "PRIVATE") || strings.Count(string(data), `"unavailable":true`) != 3 || !strings.Contains(string(data), `"kind":"quota"`) {
		t.Fatal("log API failure lost safe partial evidence or skipped resources")
	}
}
