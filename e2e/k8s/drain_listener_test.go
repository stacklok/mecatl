//go:build kind_e2e

package k8s_e2e_test

import (
	"fmt"
	"net/http"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func drainIsolationSpecs() {
	ginkgo.Describe("drain listener isolation", func() {
		ginkgo.It("does not expose drain through normal Service HTTP traffic", ginkgo.SpecTimeout(60*time.Second), func(ctx ginkgo.SpecContext) {
			ginkgo.By("verifying the Service exposes only its grpc and HTTP target ports")
			gomega.Expect(agentServicePorts()).To(gomega.ConsistOf(
				servicePort{Name: "grpc", Port: 8080, TargetPort: "grpc"},
				servicePort{Name: "http", Port: 8081, TargetPort: "http"},
			))

			ginkgo.By("port-forwarding the normal HTTP Service port")
			addr, stop := portForwardService()
			defer stop()

			ginkgo.By("requesting /drain through the Service")
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/drain", addr), nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			resp, err := http.DefaultClient.Do(req)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			_ = resp.Body.Close()
			gomega.Expect(resp.StatusCode).To(gomega.Equal(http.StatusNotFound))

			ginkgo.By("confirming both healthy replicas remain Service endpoints")
			gomega.Eventually(serviceReadyEndpointCount, 30*time.Second, time.Second).Should(gomega.Equal(2))
			gomega.Consistently(serviceReadyEndpointCount, 2*time.Second, 500*time.Millisecond).Should(gomega.Equal(2))
		})
	})
}
