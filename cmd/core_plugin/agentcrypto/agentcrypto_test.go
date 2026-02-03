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

package agentcrypto

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
)

func TestNewModule(t *testing.T) {
	module := NewModule(context.Background())
	if module.ID != moduleID {
		t.Errorf("NewModule() returned module with ID %q, want %q", module.ID, moduleID)
	}
	if module.BlockSetup == nil {
		t.Errorf("NewModule() returned module with nil BlockSetup")
	}
	if module.Setup != nil {
		t.Errorf("NewModule() returned module with Setup not nil, want nil")
	}
	if module.Description == "" {
		t.Errorf("NewModule() returned module with empty Description")
	}
}

const (
	defaultTemplate = `
	{
			"instance": {
				"attributes": {
					"hostname": "test"
				}
			},
			"project": {
				"attributes": {
					"hostname": "test"
				}
			}
	}`

	instanceHTTPSTemplate = `
	{
		"instance":  {
			"attributes": {
				"disable-https-mds-setup": %q
			}
		},
		"project": {
			"attributes": {
			}
		}
	}`

	instanceNativeStoreTemplate = `
	{
		"instance":  {
			"attributes": {
				"enable-https-mds-native-cert-store": %q
			}
		},
		"project": {
			"attributes": {
			}
		}
	}`

	projectHTTPSTemplate = `
	{
		"instance": {
			"attributes": {
			}
		},
		"project":  {
			"attributes": {
				"disable-https-mds-setup": %q
			}
		}
	}`

	projectNativeStoreTemplate = `
	{
		"instance": {
			"attributes": {
			}
		},
		"project":  {
			"attributes": {
				"enable-https-mds-native-cert-store": %q
			}
		}
	}`

	bothHTTPSTemplate = `
	{
		"instance":  {
			"attributes": {
				"disable-https-mds-setup": %q
			}
		},
		"project":  {
			"attributes": {
				"disable-https-mds-setup": %q
			}
		}
	}`

	bothNativeStoreTemplate = `
	{
		"instance":  {
			"attributes": {
				"enable-https-mds-native-cert-store": %q
			}
		},
		"project":  {
			"attributes": {
				"enable-https-mds-native-cert-store": %q
			}
		}
	}`
)

func buildDescriptor(t *testing.T, instanceAttr, projectAttr, template string) *metadata.Descriptor {
	t.Helper()

	if instanceAttr != "" && projectAttr != "" {
		desc, err := metadata.UnmarshalDescriptor(fmt.Sprintf(template, instanceAttr, projectAttr))
		if err != nil {
			t.Fatalf("metadata.UnmarshalDescriptor(%s) failed unexpectedly with error: %v", fmt.Sprintf(template, instanceAttr, projectAttr), err)
		}
		return desc
	}

	if instanceAttr != "" {
		desc, err := metadata.UnmarshalDescriptor(fmt.Sprintf(template, instanceAttr))
		if err != nil {
			t.Fatalf("metadata.UnmarshalDescriptor(%s) failed unexpectedly with error: %v", fmt.Sprintf(template, instanceAttr), err)
		}
		return desc
	}

	if projectAttr != "" {
		desc, err := metadata.UnmarshalDescriptor(fmt.Sprintf(template, projectAttr))
		if err != nil {
			t.Fatalf("metadata.UnmarshalDescriptor(%s) failed unexpectedly with error: %v", fmt.Sprintf(template, projectAttr), err)
		}
		return desc
	}

	desc, err := metadata.UnmarshalDescriptor(defaultTemplate)
	if err != nil {
		t.Fatalf("metadata.UnmarshalDescriptor(%s) failed unexpectedly with error: %v", defaultTemplate, err)
	}

	return desc
}

func TestUseNativeStore(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	tests := []struct {
		name   string
		mds    *metadata.Descriptor
		cfgVal *cfg.MDS
		want   bool
	}{
		{
			name:   "defaults",
			mds:    buildDescriptor(t, "", "", ""),
			cfgVal: cfg.Retrieve().MDS,
			want:   false,
		},
		{
			name:   "enable_from_cfg",
			mds:    buildDescriptor(t, "", "", ""),
			cfgVal: &cfg.MDS{HTTPSMDSEnableNativeStore: true},
			want:   true,
		},
		{
			name: "enable_from_instance_attr",
			mds:  buildDescriptor(t, "true", "", instanceNativeStoreTemplate),
			want: true,
		},
		{
			name: "disable_from_instance_attr",
			mds:  buildDescriptor(t, "false", "", instanceNativeStoreTemplate),
			want: false,
		},
		{
			name: "enable_from_project_attr",
			mds:  buildDescriptor(t, "", "true", projectNativeStoreTemplate),
			want: true,
		},
		{
			name: "disable_from_project_attr",
			mds:  buildDescriptor(t, "", "false", projectNativeStoreTemplate),
			want: false,
		},
		{
			name: "enable_instance_disable_project_attr",
			mds:  buildDescriptor(t, "true", "false", bothNativeStoreTemplate),
			want: true,
		},
		{
			name: "enable_project_disable_instance_attr",
			mds:  buildDescriptor(t, "false", "true", bothNativeStoreTemplate),
			want: false,
		},
		{
			name:   "enable_both_attr_disable_cfg",
			mds:    buildDescriptor(t, "true", "true", bothNativeStoreTemplate),
			cfgVal: &cfg.MDS{HTTPSMDSEnableNativeStore: false},
			want:   true,
		},
		{
			name:   "disable_both_attr_enable_cfg",
			mds:    buildDescriptor(t, "false", "false", bothNativeStoreTemplate),
			cfgVal: &cfg.MDS{HTTPSMDSEnableNativeStore: true},
			want:   false,
		},
		{
			name:   "enable_proj_cfg_attr_disable_instance_attr",
			mds:    buildDescriptor(t, "false", "true", bothNativeStoreTemplate),
			cfgVal: &cfg.MDS{HTTPSMDSEnableNativeStore: true},
			want:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg.Retrieve().MDS = test.cfgVal
			if got := useNativeStore(test.mds); got != test.want {
				t.Errorf("shouldUseNativeStore(%+v) = %t, want %t", test.mds, got, test.want)
			}
		})
	}
}

type contextKey int

const (
	// MDSOverride is the context key used by fake MDS for getting test
	// conditions.
	MDSOverride contextKey = iota
)

// MDSClient implements fake metadata server.
type MDSClient struct {
	desc *metadata.Descriptor
}

// GetKeyRecursive implements fake GetKeyRecursive MDS method.
func (s MDSClient) GetKeyRecursive(ctx context.Context, key string) (string, error) {
	return "", fmt.Errorf("GetKeyRecursive() not yet implemented")
}

// Get method implements fake Get on MDS.
func (s MDSClient) Get(ctx context.Context) (*metadata.Descriptor, error) {
	switch ctx.Value(MDSOverride) {
	case "succeed":
		return s.desc, nil
	case "fail_mds_connect":
		return nil, fmt.Errorf("this is fake MDS error")
	default:
		return nil, nil
	}
}

// Watch method implements fake watcher on MDS.
func (s MDSClient) Watch(context.Context) (*metadata.Descriptor, error) {
	return nil, fmt.Errorf("not yet implemented")
}

// WriteGuestAttributes method implements fake writer on MDS.
func (s MDSClient) WriteGuestAttributes(context.Context, string, string) error {
	return fmt.Errorf("not yet implemented")
}

// GetKey implements fake GetKey MDS method.
func (s MDSClient) GetKey(ctx context.Context, key string, headers map[string]string) (string, error) {
	valid := `
  {
    "encrypted_credentials": "q3u9avkCiXCgKAopiG3WFKmIfwidMq+ISLEIufPDBq0EdVRt+5XnEqz1dJyNuqdeRNmP24VlsXaZ77wQtF/6qcg4t0JhUqn18VkodIUvhz8zFdYGe9peu5EprcC/h8MvSrKXS6WmWRn1920/itPo4yPKl31mOGaOwRuPYqNLVUUu1iFZZ3VZTTDp5yh3AyvLoO41UoKi6siZM+xo+PB+qoHcARGctvNfsZv+jZYbAh6PRuJ2kI4aBBp2sUFWQhAZOoDYqLpcrtTe1d9LeQC/PN/PVz5FiLOwu87YsnOGgt7/K1ce2AxDGRJaINHarricVXaqx38h0u8zei7ynTsSZIemNo9SoR6dH7feRaSiH23htHryJQMx8TV32XHzuE0GdApTLkHIqc0eZGmoJ/PGYy6INaVC+kpk+7tlZ3ZwkKneXgroyy20Iig+wfKMcj8i7ncLP01PMep9d7uFaCuoshdxJbAEeqPCNr59D7zfRBDg+QBavLKv3aPSMqFOYF1tqj2mOB1EHsasZgtDslSwDN7EhkR2YbBi2HNSNFKzEnh5SsbXINSyAgaffoK+99YrLRXCQpdaqr9GIRug6HzMzQMsXhIxr4yErVbpPcv7GSC21vi4PWU62zhvWUZ8w4HXds3HjvpJk3ILrglM72xfkddEdr1Hd7KP1F3h6nG+9FFP4s6Z6j7uHPrL+ppd7Od4dDc05hA+Unifoyshb+IaCJGtzewQtofLhyZcoEZBzp1iMT5IwSCZm6eHSwCG9hS7S9eKJAcjLBwSxWZhwO4UXU3mJM0ZTZfxUxXtmR9Ombpm5xpIu5fa4rMi1DUCKK2vrYDR5hYJrEUsFLzyK+4EGuWz+FPgMXi6gXMZZYVQCjS3zcnfBsEL18EvlDHs2muuHWE/gEjGO0nFCUFuNwkOY2bW+BU8/eKwosYxYhQk+jwYJFEuSXqtm+wgCEyFvIbg42GDc+YrKPTxAzWiBH/RL/XrPR4InDZ6extmSYZbneLjT1YRAAfLR/MOiWuY2I38Q2VYBzMqZ6y1/1EgToNMW2viYlxEVmN1ys0msospzxCGwlR0DWkSzEDJmYT2SQcKFC9OrdMZ2o6BD4s315M8lv5v7ZsL7KuoYNZ4gMBN6MrxJYD6OwdLeytCmI71LdvgVw5gdDmoChu9dFDyzPKSoMYJnvTr5ktrYwxZIyWn8Sl3BjAaslZkAwL+c5oijCTCZ+oV9vzdD7tBnFx9y3fVVFtMC3nflyEjInEUPCupxh38O4TsYLLVl7tttL696kUKdlHL1SRAFCX1Wb5p4WNSBzQQtTGU1dsw904CncAj32sW32oGFWqb4Bom1OzoV/equ32Anef8J95mF+ahmf1BvTUMUq5Az2mSi2/dFBhuhy7rhGQyVWpwCEzpzVpVlysDr5aWr8CLbDOLzJv3MIDM3QQ=",
    "key_import_blob": {
      "duplicate": "ACAFYwCs8qzuSCCTvS1iCIHVTDuEXrP7WNNYPGl44ZPARLbhYVWaSkttYk1J2ChEEwG+u0fRxBVF95nEbe3xzN17+pppFFKelB9Jlf+PybtE0rRMyIJ0CB4HT9w=",
      "encrypted_seed": "ACBnqcxLycU+VUxeB89a7DCa0BSqOciydCReXia87EDLjQAgEUyXgTSjqA4tOxRNARnW5fw4B2p6AJFLD1nZx+llJP8=",
      "public_area": "AAgACwAAAEAAAAAQACCmhjk4ZFa6nbv58ya74lshnfNfGaCta6+hPIR5s+hZBw=="
    }
  }
  `

	invalid := `
  {
    "encrypted_credentials": "q3u9avkCLOwu87YsnOmNo9SoR6d/dFBhuhy7rhGQyVWpwCEzpzVpVlysDr5aWr8CLbDOLzJv3MIDM3QQ=",
    "key_import_blob": {
      "duplicate": "ACAFYwCs8qzuSCCTvS1iCIHVT9Jlf+PybtE0rRMyIJ0CB4HT9w=",
      "encrypted_seed": "ACBnqcxLycU+VUxeB8D1nZx+llJP8=",
      "public_area": "AAgACwAAAEAAAA+hPIR5s+hZBw=="
    }
  }
  `

	switch ctx.Value(MDSOverride) {
	case "succeed":
		return valid, nil
	case "fail_mds_connect":
		return "", fmt.Errorf("this is fake MDS error")
	case "fail_unmarshal":
		return invalid, nil
	default:
		return "", nil
	}
}

func TestShouldEnableMTLS(t *testing.T) {
	ctx := context.Background()
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	defCfg := cfg.Retrieve().MDS

	tests := []struct {
		name        string
		mds         *metadata.Descriptor
		cfgVal      *cfg.MDS
		overrideKey string
		previousErr bool
		currentErr  bool
		want        bool
	}{
		{
			name:   "defaults",
			mds:    buildDescriptor(t, "", "", ""),
			cfgVal: defCfg,
			want:   true,
		},
		{
			name:   "enable_from_cfg",
			mds:    buildDescriptor(t, "", "", ""),
			cfgVal: &cfg.MDS{DisableHTTPSMdsSetup: false},
			want:   true,
		},
		{
			name: "enable_from_instance_attr",
			mds:  buildDescriptor(t, "false", "", instanceHTTPSTemplate),
			want: true,
		},
		{
			name: "disable_from_instance_attr",
			mds:  buildDescriptor(t, "true", "", instanceHTTPSTemplate),
			want: false,
		},
		{
			name: "enable_from_project_attr",
			mds:  buildDescriptor(t, "", "false", projectHTTPSTemplate),
			want: true,
		},
		{
			name: "disable_from_project_attr",
			mds:  buildDescriptor(t, "", "true", projectHTTPSTemplate),
			want: false,
		},
		{
			name: "enable_instance_disable_project_attr",
			mds:  buildDescriptor(t, "false", "true", bothHTTPSTemplate),
			want: true,
		},
		{
			name: "enable_project_disable_instance_attr",
			mds:  buildDescriptor(t, "true", "false", bothHTTPSTemplate),
			want: false,
		},
		{
			name:   "enable_both_attr_disable_cfg",
			mds:    buildDescriptor(t, "false", "false", bothHTTPSTemplate),
			cfgVal: &cfg.MDS{DisableHTTPSMdsSetup: false},
			want:   true,
		},
		{
			name:   "disable_both_attr_enable_cfg",
			mds:    buildDescriptor(t, "true", "true", bothHTTPSTemplate),
			cfgVal: &cfg.MDS{DisableHTTPSMdsSetup: true},
			want:   false,
		},
		{
			name:   "enable_proj_cfg_attr_disable_instance_attr",
			mds:    buildDescriptor(t, "true", "false", bothHTTPSTemplate),
			cfgVal: &cfg.MDS{DisableHTTPSMdsSetup: false},
			want:   false,
		},
		{
			name:        "enable_both_no_key_reachable",
			mds:         buildDescriptor(t, "false", "false", bothHTTPSTemplate),
			overrideKey: "fail_mds_connect",
			cfgVal:      &cfg.MDS{DisableHTTPSMdsSetup: false},
			want:        false,
			previousErr: false,
			currentErr:  true,
		},
		{
			name:        "enable_both_key_reachable",
			mds:         buildDescriptor(t, "false", "false", bothHTTPSTemplate),
			overrideKey: "succeed",
			cfgVal:      &cfg.MDS{DisableHTTPSMdsSetup: false},
			want:        true,
			previousErr: true,
			currentErr:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg.Retrieve().MDS = test.cfgVal
			ctx = context.WithValue(ctx, MDSOverride, test.overrideKey)
			handler := &moduleHandler{metadata: &MDSClient{}}
			handler.failedPrevious.Store(test.previousErr)

			if got := handler.enableJob(ctx, test.mds); got != test.want {
				t.Errorf("enableJob(ctx, %+v) = %t, want %t", test.mds, got, test.want)
			}

			if test.overrideKey == "" {
				return
			}

			if got := handler.failedPrevious.Load(); got != test.currentErr {
				t.Errorf("enableJob set failedPrevious = %t, want %t", got, test.currentErr)
			}
		})
	}
}

func TestCallbackHandlerError(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	tests := []struct {
		name   string
		evData *events.EventData
	}{
		{
			name:   "event_error",
			evData: &events.EventData{Error: fmt.Errorf("test error")},
		},
		{
			name:   "invalid_event_type",
			evData: &events.EventData{Data: "test_data"},
		},
	}

	ctx := context.Background()

	for _, tc := range tests {
		m := &moduleHandler{}
		got, noop, err := m.eventCallback(ctx, "test_event", nil, tc.evData)
		if err == nil {
			t.Errorf("callbackHandler(test_event, nil, %v) returned error: [%v], want error: %t", tc.evData, err, true)
		}
		if !noop {
			t.Errorf("callbackHandler(test_event, nil, %v) returned noop: %t, want noop: %t", tc.evData, noop, false)
		}
		if !got {
			t.Errorf("callbackHandler(test_event, nil, %v) = continue: %t, want: %t", tc.evData, got, true)
		}
	}
}

func TestCallbackHandler(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}
	ctx := context.Background()

	tests := []struct {
		name         string
		ev           *events.EventData
		jobScheduled bool
	}{
		{
			name:         "enable_job",
			ev:           &events.EventData{Data: buildDescriptor(t, "false", "", instanceHTTPSTemplate)},
			jobScheduled: true,
		},
		{
			name:         "disable_job",
			ev:           &events.EventData{Data: buildDescriptor(t, "true", "", instanceHTTPSTemplate)},
			jobScheduled: false,
		},
	}

	// Run tests in sequence for validating the job scheduling.
	for _, tc := range tests {
		m := &moduleHandler{metadata: &MDSClient{}}
		got, noop, err := m.eventCallback(ctx, "test_event", nil, tc.ev)
		if err != nil {
			t.Errorf("callbackHandler(test_event, nil, %v) returned error: [%v], want error: %t", tc.ev, err, false)
		}
		if noop {
			t.Errorf("callbackHandler(test_event, nil, %v) returned noop: %t, want noop: %t", tc.ev, noop, false)
		}
		if !got {
			t.Errorf("callbackHandler(test_event, nil, %v) = continue: %t, want: %t", tc.ev, got, false)
		}
		if scheduler.Instance().IsScheduled(MTLSSchedulerID) != tc.jobScheduled {
			t.Errorf("callbackHandler(test_event, nil, %v) scheduled job: %t, want: %t", tc.ev, scheduler.Instance().IsScheduled(MTLSSchedulerID), tc.jobScheduled)
		}
	}
}

func TestSetup(t *testing.T) {
	if err := cfg.Load(nil); err != nil {
		t.Fatalf("cfg.Load(nil) failed unexpectedly with error: %v", err)
	}

	desc := buildDescriptor(t, "false", "", instanceHTTPSTemplate)
	credsDir := t.TempDir()
	m := &moduleHandler{metadata: &MDSClient{desc: desc}, credsDir: credsDir}
	ctx := context.Background()

	t.Cleanup(scheduler.Instance().Stop)

	files := []string{rootCACertFileName, clientCredsFileName}
	var presentAfterCleanup []string

	if runtime.GOOS == "windows" {
		files = append(files, "mds-mtls-client.key.pfx")
		presentAfterCleanup = []string{"other_file"}
	}

	credsExist := func() bool {
		if runtime.GOOS == "linux" {
			return file.Exists(credsDir, file.TypeDir)
		}

		for _, f := range files {
			if file.Exists(filepath.Join(credsDir, f), file.TypeFile) {
				t.Logf("File %q exists", filepath.Join(credsDir, f))
				return true
			}
		}
		return false
	}

	createCreds := func() {
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(credsDir, f), []byte("test"), 0644); err != nil {
				t.Fatalf("os.WriteFile() failed unexpectedly with error: %v", err)
			}
		}
		for _, f := range presentAfterCleanup {
			if err := os.WriteFile(filepath.Join(credsDir, f), []byte("test"), 0644); err != nil {
				t.Fatalf("os.WriteFile() failed unexpectedly with error: %v", err)
			}
		}
	}

	tests := []struct {
		name        string
		overrideKey string
		credsExist  bool
	}{
		{
			name:        "success",
			overrideKey: "succeed",
			credsExist:  true,
		},
		{
			name:        "error",
			overrideKey: "fail_mds_connect",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx = context.WithValue(ctx, MDSOverride, tc.overrideKey)
			if tc.credsExist {
				createCreds()
			}

			if err := m.setup(ctx, nil); err != nil {
				t.Errorf("Setup() failed unexpectedly with error: %v", err)
			}
			if !events.FetchManager().IsSubscribed(metadata.LongpollEvent, moduleID) {
				t.Errorf("Setup() did not subscribe to longpoll event")
			}
			if credsExist() {
				t.Errorf("Setup() did not clean up credentials directory: %q", credsDir)
			}
			for _, f := range presentAfterCleanup {
				if !file.Exists(filepath.Join(credsDir, f), file.TypeFile) {
					t.Errorf("Setup() did not preserve file: %q", f)
				}
			}
		})
	}
}
