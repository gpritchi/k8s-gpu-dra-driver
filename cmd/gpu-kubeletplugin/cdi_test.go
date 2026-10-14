/*
Copyright (c) Advanced Micro Devices, Inc. All rights reserved.

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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1beta1"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdispec "tags.cncf.io/container-device-interface/specs-go"
)

// The common CDI spec must carry the node name that the driver parsed into its
// config flag, not whatever NODE_NAME happens to be in the process environment,
// so it stays consistent with the kubeletplugin and the ResourceSlice pool. The
// test drives NewCDIHandler so it guards the config -> handler -> spec wiring, and
// sets a conflicting NODE_NAME to prove the environment no longer wins.
func TestCreateCommonSpecFileUsesConfiguredNodeName(t *testing.T) {
	t.Setenv("NODE_NAME", "node-from-environment")

	cdiRoot := t.TempDir()
	config := &Config{
		flags: &Flags{
			cdiRoot:  cdiRoot,
			nodeName: "node-from-config",
		},
	}

	handler, err := NewCDIHandler(config)
	require.NoError(t, err)
	require.NoError(t, handler.CreateCommonSpecFile())

	entries, err := os.ReadDir(cdiRoot)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	spec, err := cdiapi.ReadSpec(filepath.Join(cdiRoot, entries[0].Name()), 0)
	require.NoError(t, err)
	require.Len(t, spec.Devices, 1)

	require.Contains(t, spec.Devices[0].ContainerEdits.Env, "KUBERNETES_NODE_NAME=node-from-config")
	require.NotContains(t, spec.Devices[0].ContainerEdits.Env, "KUBERNETES_NODE_NAME=node-from-environment")
}

func TestCDIDeviceName(t *testing.T) {
	slot := 2
	tests := map[string]struct {
		device *PreparedDevice
		want   string
	}{
		"plain GPU": {
			device: &PreparedDevice{Device: drapbv1.Device{DeviceName: "gpu-1-128"}, ShareKey: "uid/gpu/gpu-1-128/"},
			want:   "uid-gpu-1-128",
		},
		"no share key (checkpoint from an older driver)": {
			device: &PreparedDevice{Device: drapbv1.Device{DeviceName: "gpu-1-128"}},
			want:   "uid-gpu-1-128",
		},
		"share of a whole GPU": {
			device: &PreparedDevice{Device: drapbv1.Device{DeviceName: "gpu-1-128"}, ShareKey: "uid/gpu/gpu-1-128/abc"},
			want:   "uid-gpu-1-128-share-abc",
		},
		"partition slot wins over share": {
			device: &PreparedDevice{Device: drapbv1.Device{DeviceName: "gpu-0-cpx-nps4"}, ShareKey: "uid/gpu/gpu-0-cpx-nps4/abc", PartitionSlot: &slot},
			want:   "uid-gpu-0-cpx-nps4-slot2",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, test.want, cdiDeviceName("uid", test.device))
		})
	}
}

// One claim with two requests can be allocated two shares of the same whole GPU
// (WholeGPUSharing). The CDI spec must hold one device per share; duplicate
// names make WriteSpec reject the spec and the pod never starts.
func TestCreateClaimSpecFile_TwoSharesOfOneGPU(t *testing.T) {
	cdiRoot := t.TempDir()
	handler, err := NewCDIHandler(&Config{flags: &Flags{cdiRoot: cdiRoot, nodeName: "node-a"}})
	require.NoError(t, err)

	edits := func() *cdiapi.ContainerEdits {
		return &cdiapi.ContainerEdits{ContainerEdits: &cdispec.ContainerEdits{Env: []string{"SHARE=1"}}}
	}
	devices := PreparedDevices{
		{Device: drapbv1.Device{RequestNames: []string{"a"}, DeviceName: "gpu-1-128"}, ShareKey: "claim-uid/a/gpu-1-128/share-a", ContainerEdits: edits()},
		{Device: drapbv1.Device{RequestNames: []string{"b"}, DeviceName: "gpu-1-128"}, ShareKey: "claim-uid/b/gpu-1-128/share-b", ContainerEdits: edits()},
	}
	require.NoError(t, handler.CreateClaimSpecFile("claim-uid", devices))

	ids := handler.GetClaimDevices("claim-uid", devices)
	require.Len(t, ids, 3) // common + one per share
	require.NotEqual(t, ids[1], ids[2])
}
