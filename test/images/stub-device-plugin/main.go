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

package main

import (
	"flag"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"

	"github.com/fsnotify/fsnotify"
	"github.com/golang/glog"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1alpha"
)

var (
	resourcePath = ""
	endpointPath = ""
)

func init() {
	defaultEndpointPath := pluginapi.DevicePluginPath + "stub.sock"
	flag.StringVar(&resourcePath, "resource-path", "dummy-device/resource.name-1", "Path to read resource name and dummy device file from")
	flag.StringVar(&endpointPath, "endpoint-path", defaultEndpointPath, "Path to stub device plugin grpc endpoint")
}

// dummy device directory looks like this:
// |-- tmp
//     |-- dummy-device
//         |-- resource.name-1  # hacky here, replace "-" with "/" to follow kubernetes resource naming convention
//             |-- device-1 (content: Healthy)
//             |-- device-2 (content: Unhealthy)
//         |-- resource.name-2
//             |-- device-1 (content: Healthy)
//             |-- device-2 (content: Healthy)
//             |-- device-3 (content: Unhealthy)
//         |-- resource.name-3
//             |-- device-1 (content: Healthy)
//             |-- device-2 (content: Healthy)
func createStubDevicePluginOrDie() *StubDevicePlugin {
	// get resource name from resourcePath
	// e.g. resourcePath is dummy-device/resource.name-1
	// get resourceName resource.name/1
	_, subDir := path.Split(resourcePath)
	// TODO(cph): hacky here, replace "-" with "/" to follow kubernetes resource naming convention
	resourceName := strings.Replace(subDir, "-", "/", -1)

	// create new stub device plugin with endpoint and resource device path
	resourceDevicePath := path.Join(os.TempDir(), resourcePath)
	stubDevicePlugin, err := NewStubDevicePlugin(endpointPath, resourceDevicePath, resourceName)
	if err != nil {
		glog.Fatalf("Failed to create stub device plugin: %v", err)
	}

	return stubDevicePlugin
}

func main() {
	flag.Parse()

	stubDevicePlugin := createStubDevicePluginOrDie()

	// start serving
	stubDevicePlugin.Start()

	// watcher for Kubelet
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		glog.Fatal(err)
	}

	defer watcher.Close()
	err = watcher.Add(pluginapi.DevicePluginPath)
	if err != nil {
		glog.Fatal(err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

L:
	for {
		restart := false
		select {
		// kubelet socket changed
		case event := <-watcher.Events:
			if event.Name == pluginapi.KubeletSocket &&
				(event.Op&fsnotify.Create == fsnotify.Create ||
					event.Op&fsnotify.Write == fsnotify.Write) {
				glog.Infof("inotify: %s changed, restarting", pluginapi.KubeletSocket)
				restart = true
			}
		case err := <-watcher.Errors:
			glog.Infof("inotify: %s", err)
		// system sigs
		case s := <-sigs:
			switch s {
			case syscall.SIGHUP:
				glog.Info("Received SIGHUP, restarting")
				restart = true
			default:
				glog.Infof("Received signal: %d, shutting down", s)
				stubDevicePlugin.Stop()
				break L
			}
		}
		if restart {
			glog.Info("restarting...")
			stubDevicePlugin.Stop()
			stubDevicePlugin = createStubDevicePluginOrDie()
			stubDevicePlugin.Start()
		}
	}
}
