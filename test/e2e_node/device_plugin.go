/*
Copyright 2017 The Kubernetes Authors.

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

package e2e_node

import (
	"strings"
	"time"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/kubelet/apis/kubeletconfig"
	"k8s.io/kubernetes/test/e2e/framework"

	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1alpha"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const (
	// dummy device file saved in "/tmp/dummy-device" directory,
	// e.g. "/tmp/dummy-device/device-1", "/tmp/dummy-device/device-2"
	dummyDeviceDir string = "dummy-device"
	dummyDeviceRE  string = "^device-[0-9]*$"

	// in this test, we expected there are numberOfStubDevices devices existed
	numberOfStubDevices int64 = 2
)

var resourceName string
var resourcePath string

// Serial because the test restarts Kubelet
var _ = framework.KubeDescribe("Device Plugin [Feature:DevicePlugin] [Serial] [Disruptive]", func() {
	f := framework.NewDefaultFramework("device-plugin-errors")

	Context("DevicePlugin", func() {
		By("Enabling support for Device Plugin")
		tempSetCurrentKubeletConfig(f, func(initialConfig *kubeletconfig.KubeletConfiguration) {
			initialConfig.FeatureGates[string(features.DevicePlugins)] = true
		})

		BeforeEach(func() {
			By("Create dummy device files on the node")
			resources, err := framework.CreateDummyDeviceFileFromConfig()

			if err != nil || len(resources) == 0 {
				Skip("Failed to create dummy device files. Skipping test.")
			}

			// TODO(cph): for now use the first resource
			// we can simulate more resources registration scenario by extending this
			resourceName = resources[0].ResourceName
			resourcePath = resources[0].ResourcePath

			By("Creating stub device plugin pod")
			f.PodClient().CreateSync(framework.StubDevicePlugin(f.Namespace.Name, resourcePath))

			By("Wait for node is ready")
			Eventually(func() int {
				nodeList := framework.GetReadySchedulableNodesOrDie(f.ClientSet)
				return len(nodeList.Items)
			}, 30*time.Second, framework.Poll).Should(Equal(1))

			By("Waiting for stub device to become available on the local node")
			Eventually(func() bool {
				return framework.NumberOfStubDevices(getLocalNode(f), resourceName) > 0
			}, 30*time.Second, framework.Poll).Should(BeTrue())

			if framework.NumberOfStubDevices(getLocalNode(f), resourceName) < numberOfStubDevices {
				Skip("Not enough dummy device to execute this test (at least two needed)")
			}
		})

		AfterEach(func() {
			l, err := f.PodClient().List(metav1.ListOptions{})
			framework.ExpectNoError(err)

			for _, p := range l.Items {
				if p.Namespace != f.Namespace.Name {
					continue
				}

				f.PodClient().Delete(p.Name, &metav1.DeleteOptions{})
			}
		})

		It("Checks that when Kubelet restarts exclusive dummy-device assignation to pods is kept.", func() {
			By("Creating one dummy-device pod on a node with at least two dummy-devices")
			p1 := f.PodClient().CreateSync(makeStubPauseImage(resourceName))
			deviceRE := "stub devices: (device-[0-9]+)"
			devId1 := framework.GetDeviceIdWithRE(f, p1.Name, p1.Name, 0, deviceRE)
			Expect(devId1).To(Not(Equal("")))
			p1, err := f.PodClient().Get(p1.Name, metav1.GetOptions{})
			framework.ExpectNoError(err)

			By("Restarting Kubelet and waiting for the current running pod to restart")
			socketRE := pluginapi.DevicePluginPath + "stub.sock-*"
			framework.RestartKubeletWithSocketRE(socketRE)

			By("Wait for node is ready")
			Eventually(func() int {
				nodeList := framework.GetReadySchedulableNodesOrDie(f.ClientSet)
				return len(nodeList.Items)
			}, 30*time.Second, framework.Poll).Should(Equal(1))

			By("Confirming that after a kubelet and pod restart, dummy-device assignement is kept")
			devIdRestart := framework.GetDeviceIdWithRE(f, p1.Name, p1.Name, 1, deviceRE)
			Expect(devIdRestart).To(Equal(devId1))

			By("Restarting Kubelet and creating another pod")
			framework.RestartKubeletWithSocketRE(socketRE)

			// TODO(cph): remove sleep here will break test
			time.Sleep(30 * time.Second)

			p2 := f.PodClient().CreateSync(makeStubPauseImage(resourceName))

			By("Checking that pods got a different dummy-device")
			devId2 := framework.GetDeviceIdWithRE(f, p2.Name, p2.Name, 0, deviceRE)
			p2, err = f.PodClient().Get(p2.Name, metav1.GetOptions{})
			framework.ExpectNoError(err)

			Expect(devId2).To(Not(Equal("")))
			Expect(devId2).To(Not(Equal(devId1)))

			// Cleanup
			f.PodClient().DeleteSync(p1.Name, &metav1.DeleteOptions{}, framework.DefaultPodDeletionTimeout)
			f.PodClient().DeleteSync(p2.Name, &metav1.DeleteOptions{}, framework.DefaultPodDeletionTimeout)
		})
	})
})

func makeStubPauseImage(resourceName string) *v1.Pod {
	podName := "device-plugin-test-" + string(uuid.NewUUID())
	privileged := true
	subdir := strings.Replace(resourceName, "/", "-", -1)

	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyAlways,
			Containers: []v1.Container{{
				Image: busyboxImage,
				Name:  podName,
				// Retrieves the stub devices created in the test pod.
				Command: []string{"sh", "-c", "devs=$(ls /tmp/" + framework.DummyDeviceDir + "/" + subdir + " | egrep '^device-[0-9]+$') && echo stub devices: $devs"},
				Resources: v1.ResourceRequirements{
					Limits:   framework.NewDecimalResourceList(v1.ResourceName(resourceName), 1),
					Requests: framework.NewDecimalResourceList(v1.ResourceName(resourceName), 1),
				},
				SecurityContext: &v1.SecurityContext{
					Privileged: &privileged,
				},
			}},
		},
	}
}
