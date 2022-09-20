/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/pointer"

	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/cluster-api/util"
)

// DeployAppSpec implements a test that verifies that an app deployed to the workload cluster works.
func DeployAppToxiSpec(ctx context.Context, inputGetter func() CommonSpecInput) {
	var (
		specName                  = "deploy-app-toxi"
		input                     CommonSpecInput
		namespace                 *corev1.Namespace
		cancelWatches             context.CancelFunc
		clusterResources          *clusterctl.ApplyClusterTemplateAndWaitResult
		appName                   = "httpd"
		appManifestPath           = "data/fixture/sample-application.yaml"
		expectedHtmlPath          = "data/fixture/expected-webpage.html"
		appDeploymentReadyTimeout = 180
		appPort                   = 8080
		appDefaultHtmlPath        = "/"
		expectedHtml              = ""
		toxiProxyName             = fmt.Sprintf("deploy_app_toxi_test_%#x", rand.Intn(65535))
	)

	BeforeEach(func() {
		Expect(ctx).NotTo(BeNil(), "ctx is required for %s spec", specName)
		input = inputGetter()
		Expect(input.E2EConfig).ToNot(BeNil(), "Invalid argument. input.E2EConfig can't be nil when calling %s spec", specName)
		Expect(input.ClusterctlConfigPath).To(BeAnExistingFile(), "Invalid argument. input.ClusterctlConfigPath must be an existing file when calling %s spec", specName)
		Expect(input.BootstrapClusterProxy).ToNot(BeNil(), "Invalid argument. input.BootstrapClusterProxy can't be nil when calling %s spec", specName)
		Expect(input.ActualBootstrapClusterAddress).ToNot(BeEmpty())
		Expect(os.MkdirAll(input.ArtifactFolder, 0750)).To(Succeed(), "Invalid argument. input.ArtifactFolder can't be created for %s spec", specName)

		output, err := ToxiProxyCli(ctx, "create",
			"--listen", input.ToxiproxyBootstrapClusterAddress,
			"--upstream", input.ActualBootstrapClusterAddress,
			toxiProxyName,
		)
		if err != nil {
			fmt.Println(output)
		}
		Expect(err).To(BeNil())

		Expect(input.E2EConfig.Variables).To(HaveKey(KubernetesVersion))

		// Setup a Namespace where to host objects for this spec and create a watcher for the namespace events.
		namespace, cancelWatches = setupSpecNamespace(ctx, specName, input.BootstrapClusterProxy, input.ArtifactFolder)
		clusterResources = new(clusterctl.ApplyClusterTemplateAndWaitResult)

		fileContent, err := os.ReadFile(expectedHtmlPath)
		Expect(err).To(BeNil(), "Failed to read "+expectedHtmlPath)
		expectedHtml = string(fileContent)
	})

	It("Should be able to download an HTML from the app deployed to the workload cluster", func() {
		By("Creating a workload cluster")

		flavor := clusterctl.DefaultFlavor
		if input.Flavor != nil {
			flavor = *input.Flavor
		}
		namespace := namespace.Name
		clusterName := fmt.Sprintf("%s-%s", specName, util.RandomString(6))

		clusterctl.ApplyClusterTemplateAndWait(ctx, clusterctl.ApplyClusterTemplateAndWaitInput{
			ClusterProxy:    input.BootstrapClusterProxy,
			CNIManifestPath: input.E2EConfig.GetVariable(CNIPath),
			ConfigCluster: clusterctl.ConfigClusterInput{
				LogFolder:                filepath.Join(input.ArtifactFolder, "clusters", input.BootstrapClusterProxy.GetName()),
				ClusterctlConfigPath:     input.ClusterctlConfigPath,
				KubeconfigPath:           input.BootstrapClusterProxy.GetKubeconfigPath(),
				InfrastructureProvider:   clusterctl.DefaultInfrastructureProvider,
				Flavor:                   flavor,
				Namespace:                namespace,
				ClusterName:              clusterName,
				KubernetesVersion:        input.E2EConfig.GetVariable(KubernetesVersion),
				ControlPlaneMachineCount: pointer.Int64Ptr(1),
				WorkerMachineCount:       pointer.Int64Ptr(2),
			},
			WaitForClusterIntervals:      input.E2EConfig.GetIntervals(specName, "wait-cluster"),
			WaitForControlPlaneIntervals: input.E2EConfig.GetIntervals(specName, "wait-control-plane"),
			WaitForMachineDeployments:    input.E2EConfig.GetIntervals(specName, "wait-worker-nodes"),
		}, clusterResources)

		workloadClusterProxy := input.BootstrapClusterProxy.GetWorkloadCluster(ctx, namespace, clusterName)
		workloadKubeconfigPath := workloadClusterProxy.GetKubeconfigPath()

		appManifestAbsolutePath, _ := filepath.Abs(appManifestPath)
		Byf("Deploying a simple web server application to the workload cluster from %s", appManifestAbsolutePath)
		Expect(DeployAppToWorkloadClusterAndWaitForDeploymentReady(ctx, workloadKubeconfigPath, appName, appManifestAbsolutePath, appDeploymentReadyTimeout)).To(Succeed())

		By("Downloading the default html of the web server")
		actualHtml, err := DownloadFromAppInWorkloadCluster(ctx, workloadKubeconfigPath, appName, appPort, appDefaultHtmlPath)
		Expect(err).To(BeNil(), "Failed to download")

		Expect(actualHtml).To(Equal(expectedHtml))

		By("Confirming that the custom reconciliation error metric is scrape-able")
		// TODO: Rebuild an E2E test designed purely to test this. Adding this requirement here is too flaky.
		// Newer CloudStack instances return a different error code when the ZoneID is missing, and this test
		// keeps us from fixing the additional error message that was present when reconciling the Isolated Network
		// a bit too soon.
		// BIG NOTE: The first reconciliation attempt of isolated_network!AssociatePublicIPAddress() returns
		//  a CloudStack error 9999. This test expects that to happen.
		//  No acs_reconciliation_errors appear in the scrape until logged.
		//  If that error ever gets fixed, this test will break.
		// metricsScrape, err := DownloadMetricsFromCAPCManager(ctx, input.BootstrapClusterProxy.GetKubeconfigPath())
		// Expect(err).To(BeNil())
		// Expect(metricsScrape).To(MatchRegexp("acs_reconciliation_errors\\{acs_error_code=\"9999\"\\} [0-9]+"))
		By("PASSED!")
	})

	AfterEach(func() {
		output, err := ToxiProxyCli(ctx, "delete", toxiProxyName)
		if err != nil {
			fmt.Println(output)
		}
		Expect(err).To(BeNil())

		// Dumps all the resources in the spec namespace, then cleanups the cluster object and the spec namespace itself.
		dumpSpecResourcesAndCleanup(ctx, specName, input.BootstrapClusterProxy, input.ArtifactFolder, namespace, cancelWatches, clusterResources.Cluster, input.E2EConfig.GetIntervals, input.SkipCleanup)
	})
}
