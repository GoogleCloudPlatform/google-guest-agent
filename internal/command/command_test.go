//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os/user"
	"path"
	"runtime"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
)

func cmdServerForTest(t *testing.T, pipeMode int, pipeGroup string, timeout time.Duration) *Server {
	cs := &Server{
		pipe:      getTestPipePath(t),
		pipeMode:  pipeMode,
		pipeGroup: pipeGroup,
		timeout:   timeout,
		monitor: &Monitor{
			handlers: make(map[string]Handler),
		},
	}
	cs.monitor.srv = cs
	err := cs.start(testctx(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		err := cs.Close()
		if err != nil {
			t.Errorf("error closing command server: %v", err)
		}
	})
	return cs
}

func getTestPipePath(t *testing.T) string {
	if runtime.GOOS == "windows" {
		return `\\.\pipe\google-guest-agent-commands-test-` + t.Name()
	}
	return path.Join(t.TempDir(), "run", "pipe")
}

func testctx(t *testing.T) context.Context {
	d, ok := t.Deadline()
	if !ok {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		return ctx
	}
	ctx, cancel := context.WithDeadline(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

type testRequest struct {
	Command       string
	ArbitraryData int
}

func TestInit(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("could not load config: %v", err)
	}

	ctx := testctx(t)
	cfg.Retrieve().Unstable.CommandPipePath = getTestPipePath(t)
	cfg.Retrieve().Unstable.CommandMonitorEnabled = true
	if cmdMonitor.srv != nil {
		t.Fatal("internal command server already exists")
	}
	if err := Setup(ctx, ListenerCorePlugin); err != nil {
		t.Fatalf("could not setup command server: %v", err)
	}
	if cmdMonitor.srv == nil {
		t.Errorf("could not start internally managed command server")
	}
	Close(ctx)
}

func TestListen(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}
	cu, err := user.Current()
	if err != nil {
		t.Fatalf("could not get current user: %v", err)
	}
	ug, err := cu.GroupIds()
	if err != nil {
		t.Fatalf("could not get user groups for %s: %v", cu.Name, err)
	}
	resp := []byte(`{"Status":0,"StatusMessage":"OK"}`)
	errresp := []byte(`{"Status":1,"StatusMessage":"ERR"}`)
	req := []byte(`{"ArbitraryData":1234,"Command":"TestListen"}`)
	h := func(_ context.Context, b []byte) ([]byte, error) {
		var r testRequest
		err := json.Unmarshal(b, &r)
		if err != nil || r.ArbitraryData != 1234 {
			return errresp, nil
		}
		return resp, nil
	}

	testcases := []struct {
		name     string
		filemode int
		group    string
	}{
		{
			name:     "world read/writeable",
			filemode: 0777,
			group:    "-1",
		},
		{
			name:     "group read/writeable",
			filemode: 0770,
			group:    "-1",
		},
		{
			name:     "user read/writeable",
			filemode: 0700,
			group:    "-1",
		},
		{
			name:     "additional user group as group owner",
			filemode: 0770,
			group:    ug[rand.Intn(len(ug))],
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cs := cmdServerForTest(t, tc.filemode, tc.group, time.Second)
			err := cs.monitor.RegisterHandler("TestListen", h)
			if err != nil {
				t.Errorf("could not register handler: %v", err)
			}
			d := sendCmdPipe(testctx(t), cs.pipe, req)
			var r Response
			err = json.Unmarshal(d, &r)
			if err != nil {
				t.Error(err)
			}
			if r.Status != 0 || r.StatusMessage != "OK" {
				t.Errorf("unexpected status from test-cmd, want 0, \"OK\" but got %d, %q", r.Status, r.StatusMessage)
			}
		})
	}
}

func TestHandlerFailure(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed with error: %v", err)
	}
	req := []byte(`{"Command":"TestHandlerFailure"}`)
	h := func(_ context.Context, b []byte) ([]byte, error) {
		return nil, fmt.Errorf("always fail")
	}

	cs := cmdServerForTest(t, 0777, "-1", time.Second)
	cs.monitor.RegisterHandler("TestHandlerFailure", h)
	d := sendCmdPipe(testctx(t), cs.pipe, req)
	var r Response
	err := json.Unmarshal(d, &r)
	if err != nil {
		t.Error(err)
	}
	if r.Status != HandlerError.Status || r.StatusMessage != "always fail" {
		t.Errorf("unexpected status from TestHandlerFailure, want %d, \"always fail\" but got %d, %q", HandlerError.Status, r.Status, r.StatusMessage)
	}
}

func TestListenTimeout(t *testing.T) {
	expect := ""
	cs := cmdServerForTest(t, 0770, "-1", time.Millisecond)
	conn, err := dialPipe(testctx(t), cs.pipe)
	if err != nil {
		t.Errorf("could not connect to command server: %v", err)
	}
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Errorf("error reading response from command server: %v", err)
	}
	if string(data) != string(expect) {
		t.Errorf("unexpected response from timed out connection, got %s but want %s", data, expect)
	}
}

func TestRegisterUnregisterHandler(t *testing.T) {
	monitor := &Monitor{
		handlers: make(map[string]Handler),
	}
	handler := func(context.Context, []byte) ([]byte, error) { return nil, nil }

	if err := monitor.RegisterHandler("handlercmd", handler); err != nil {
		t.Errorf("failed to register handler: %v", err)
	}
	if f, ok := monitor.handlers["handlercmd"]; !ok || f == nil {
		t.Errorf("handlercmd doesn't exist in map after registration")
	}
	if err := monitor.RegisterHandler("handlercmd", handler); err == nil {
		t.Errorf("registered duplicate handler")
	}

	if err := monitor.UnregisterHandler("handlercmd"); err != nil {
		t.Errorf("failed to unregister handler: %v", err)
	}
	if f, ok := monitor.handlers["handlercmd"]; ok || f != nil {
		t.Errorf("handlercmd still exists in map after unregistration")
	}
	if err := monitor.UnregisterHandler("handlercmd"); err == nil {
		t.Errorf("unregisted non-existant handler")
	}
}

func TestGetOption(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) = %v, want nil", err)
	}
	testcases := []struct {
		name string
		req  []byte
		resp []byte
	}{
		{
			name: "standard_case",
			req:  []byte(`{"Command":"agent.config.getoption","Option":"InstanceSetup.NetworkEnabled"}`),
			resp: []byte(`{"Status":0,"StatusMessage":"","Option":"InstanceSetup.NetworkEnabled","Value":"true"}`),
		},
		{
			name: "unexported_field",
			req:  []byte(`{"Command":"agent.config.getoption","Option":"InstanceSetup.networkEnabled"}`),
			resp: []byte(`{"Status":3,"StatusMessage":"Invalid option, key names must start with uppercase","Option":"InstanceSetup.networkEnabled","Value":""}`),
		},
		{
			name: "non_existent_field",
			req:  []byte(`{"Command":"agent.config.getoption","Option":"BadOption"}`),
			resp: []byte(`{"Status":1,"StatusMessage":"Option does not exist","Option":"BadOption","Value":""}`),
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			b, err := getOption(ctx, tc.req)
			if err != nil {
				t.Errorf("getOption(%s) = err %v, want nil", tc.req, err)
			}
			if string(b) != string(tc.resp) {
				t.Errorf("getOption(%s) = %s want %s", tc.req, b, tc.resp)
			}
		})
	}
}
