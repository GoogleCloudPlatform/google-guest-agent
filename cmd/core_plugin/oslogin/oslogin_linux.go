//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distrbuted under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

//go:build linux

// Package oslogin contains the Linux implementation of the OS Login module.
package oslogin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/cmd/core_plugin/manager"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/cfg"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/daemon"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/events"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/metadata"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/osinfo"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/pipewatcher"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/run"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/textconfig"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"

	acmpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
)

const (
	// osloginModuleID is the ID of the OS Login module.
	osloginModuleID = "oslogin"

	// defaultPipePath is the default path to the ssh trusted ca pipe.
	defaultPipePath = "/etc/ssh/oslogin_trustedca.pub"

	// defaultPipeMode is the default mode for the ssh trusted ca pipe, it aligns
	// with the distribution's default mode for /etc/ssh/.
	defaultPipeMode = 0755

	// sshcaEventWatcherID is the ID of the ssh trusted ca pipe event watcher.
	sshcaEventWatcherID = "oslogin-sshca-pipe-event-watcher"

	// sshcaPipeWatcherReadEventID is the ID of the ssh trusted ca pipe event
	// watcher read event.
	sshcaPipeWatcherReadEventID = "oslogin-sshca-pipe-event-watcher,read"

	// defaultSSHDConfigPath is the default path to the openssh daemon
	// configuration file.
	defaultSSHDConfigPath = "/etc/ssh/sshd_config"

	// defaultNSSwitchConfigPath is the default path to the NSSwitch configuration
	// file.
	defaultNSSwitchConfigPath = "/etc/nsswitch.conf"

	// defaultPAMConfigPath is the default path to the PAM configuration file.
	defaultPAMConfigPath = "/etc/pam.d/sshd"

	// defaultGroupConfigPath is the default path to the group configuration file.
	defaultGroupConfigPath = "/etc/security/group.conf"

	// defaultSudoersPath is the default path to the sudoers file.
	defaultSudoersPath = "/etc/sudoers.d/google-sudoers"

	// defaultMaxRetryCount is the default maximum number of times to retry
	// configuration in case of retryable errors.
	defaultMaxRetryCount = 5
)

var (
	// sshcaPipeWatcherOpts are the ssh trusted ca pipe event watcher options.
	sshcaPipeWatcherOpts = pipewatcher.Options{
		PipePath:    defaultPipePath,
		Mode:        defaultPipeMode,
		ReadEventID: sshcaPipeWatcherReadEventID,
	}

	// osloginConfigMode is the mode for all OSLogin configuration files.
	osloginConfigMode = fs.FileMode(0644)

	// defaultAuthorizedKeysCommandPaths are the possible paths to the authorized
	// keyscommand binaries.
	defaultAuthorizedKeysCommandPaths = []string{
		"/usr/bin/google_authorized_keys",
		"/usr/local/bin/google_authorized_keys",
	}

	// defaultAuthorizedKeysCommandSKPaths are the possible paths to the
	// authorized keys command binaries (in the security key case/variation).
	defaultAuthorizedKeysCommandSKPaths = []string{
		"/usr/bin/google_authorized_keys_sk",
		"/usr/local/bin/google_authorized_keys_sk",
	}

	// defaultServices are the services to restart after configuration changes.
	// Each sub-array of the map indicates that only one of those services need to
	// be successfully restarted. The services are grouped by services that are
	// mutually exclusive, or exist to provide the same functionality.
	defaultServices = map[daemon.RestartMethod][]serviceRestartConfig{
		daemon.ReloadOrRestart: []serviceRestartConfig{
			{
				protocol: serviceRestartAtLeastOne,
				services: []string{"ssh", "sshd"},
			},
		},
		daemon.TryRestart: []serviceRestartConfig{
			{
				// nscd and unscd are optional because they don't come by default on
				// some platforms (like Debian or RHEL). Restarting this refreshes the
				// caches for `password`, `group`, and `hosts` databases.
				protocol: serviceRestartOptional,
				services: []string{"nscd", "unscd"},
			},
			{
				// systemd-logind is on all Linux distributions. Restarting this force
				// stops existing user sessions so users can start a new session with
				// the updated configuration.
				protocol: serviceRestartOptional,
				services: []string{"systemd-logind"},
			},
			{
				// One of cron or crond exists on all Linux distributions. Restarting
				// this forces cron to reload user information from NSS. This forces
				// cron to avoid running jobs for a user that may no longer exist.
				protocol: serviceRestartOptional,
				services: []string{"cron", "crond"},
			},
		},
	}

	// defaultOSLoginDirs are the directories to create for OSLogin, if necessary.
	defaultOSLoginDirs = []string{
		"/var/google-sudoers.d",
		"/var/google-users.d",
	}

	// defaultDeprecatedEntries are the deprecated files to clean up. This is a map
	// of the file path to the key-value pairs to remove.
	defaultDeprecatedEntries = map[string][]*textconfig.Entry{
		"/etc/pam.d/su": []*textconfig.Entry{
			textconfig.NewEntry("account", "[success=bad ignore=ignore] pam_oslogin_login.so"),
		},
	}

	// osloginConfigOpts are the oslogin configuration file options. This is used
	// in all the configuration files related to OSLogin.
	osloginConfigOpts = textconfig.Options{
		Delimiters: &textconfig.Delimiter{
			Start: "#### Google OS Login control. Do not edit this section. ####",
			End:   "#### End Google OS Login control section. ####",
		},
	}

	// execLookPath is stubbed out for testing.
	execLookPath = exec.LookPath

	// osinfoRead is stubbed out for testing.
	osinfoRead = osinfo.Read

	// sles16Map contains mapping of files to be copied from /usr to /etc for SLES 16.
	sles16Map = map[string]string{
		"/usr/etc/ssh/sshd_config":     defaultSSHDConfigPath,
		"/usr/etc/nsswitch.conf":       defaultNSSwitchConfigPath,
		"/usr/lib/pam.d/sshd":          defaultPAMConfigPath,
		"/usr/etc/security/group.conf": defaultGroupConfigPath,
	}
)

// osloginModule is the OS Login module.
type osloginModule struct {
	// prevMetadata is the previous metadata descriptor.
	prevMetadata *metadata.Descriptor
	// pipeEventHandler is the ssh trusted ca pipe event handler.
	pipeEventHandler *PipeEventHandler
	// pipeEventWatcher is the ssh trusted ca pipe event watcher.
	pipeEventWatcher *pipewatcher.Handle
	// enabled is true if the module is enabled.
	enabled atomic.Bool
	// failedConfiguration indicates if configuration setup has failed.
	failedConfiguration atomic.Bool
	// retryCount is the number of times the module has failed to configure.
	retryCount atomic.Int32
	// permanentFailure indicates if the module has failed to configure due to a
	// permanent error and should not be retried.
	permanentFailure atomic.Bool
	// sshdConfigPath is the path to the openssh daemon configuration file.
	sshdConfigPath string
	// nsswitchConfigPath is the path to the NSSwitch configuration file.
	nsswitchConfigPath string
	// pamConfigPath is the path to the PAM configuration file.
	pamConfigPath string
	// groupConfPath is the path to the group configuration file.
	groupConfigPath string
	// authorizedKeysCommandPaths are the possible paths to the authorized keys
	// command binaries.
	authorizedKeysCommandPaths []string
	// authorizedKeysCommandSKPaths are the possible paths to the authorized keys
	// command binaries (in the security key case/variation).
	authorizedKeysCommandSKPaths []string
	// services is a map of restart methods to the services to restart.
	services map[daemon.RestartMethod][]serviceRestartConfig
	// osloginDirs are the directories to create for OSLogin, if necessary.
	osloginDirs []string
	// sudoers is the path to the sudoers file.
	sudoers string
	// deprecatedEntries are the deprecated files to clean up.
	deprecatedEntries map[string][]*textconfig.Entry
	// sles16Setup is true if the SLES 16 setup has been done, this is to avoid
	// doing the setup multiple times if the module is enabled/disabled multiple
	// times or reties.
	sles16Setup atomic.Bool
}

// serviceRestartProtocol is the protocol to use when restarting a service.
type serviceRestartProtocol int

const (
	// serviceRestartAtLeastOne indicates that at least one service must be
	// successfully restarted. This is the default protocol.
	serviceRestartAtLeastOne serviceRestartProtocol = iota
	// serviceRestartOptional indicates that if all services fail to restart,
	// the configuration will still be considered successful. This is primarily
	// used for services that don't necessarily exist on all platforms.
	serviceRestartOptional
)

// serviceRestartConfig is the configuration for restarting a service.
// This is primarily a wrapper around the serviceRestartProtocol enum to make
// it easier to pass around.
type serviceRestartConfig struct {
	protocol serviceRestartProtocol
	services []string
}

// NewModule returns a new oslogin module for late registration.
func NewModule(context.Context) *manager.Module {
	module := &osloginModule{
		sshdConfigPath:               defaultSSHDConfigPath,
		nsswitchConfigPath:           defaultNSSwitchConfigPath,
		pamConfigPath:                defaultPAMConfigPath,
		groupConfigPath:              defaultGroupConfigPath,
		authorizedKeysCommandPaths:   defaultAuthorizedKeysCommandPaths,
		authorizedKeysCommandSKPaths: defaultAuthorizedKeysCommandSKPaths,
		services:                     defaultServices,
		osloginDirs:                  defaultOSLoginDirs,
		sudoers:                      defaultSudoersPath,
		deprecatedEntries:            defaultDeprecatedEntries,
	}

	return &manager.Module{
		ID:    osloginModuleID,
		Setup: module.moduleSetup,
	}
}

// moduleSetup is the setup function for the oslogin module.
func (mod *osloginModule) moduleSetup(ctx context.Context, data any) error {
	galog.Debugf("Initializing OS Login module.")
	desc, ok := data.(*metadata.Descriptor)
	if !ok {
		return fmt.Errorf("oslogin module expects a metadata descriptor in the data pointer")
	}

	if err := mod.removeDeprecatedEntries(); err != nil {
		galog.Errorf("Failed to remove deprecated entries: %v", err)
	}

	// Do the initial first setup execution in the module initialization, it will
	// be handled by the metadata longpoll event handler/subscriber after the
	// first setup.
	_, _, err := mod.osloginSetup(ctx, desc)
	if err != nil {
		galog.Errorf("Failed to handle first oslogin setup: %v", err)
	}

	// Subscribe to the metadata longpoll event.
	sub := events.EventSubscriber{Name: osloginModuleID, Callback: mod.metadataSubscriber, MetricName: acmpb.GuestAgentModuleMetric_OS_LOGIN_INITIALIZATION}
	events.FetchManager().Subscribe(metadata.LongpollEvent, sub)

	galog.Debugf("Finished initializing OS Login module.")
	return nil
}

// metadataSubscriber is the callback for the metadata event and handles the
// platform oslogin configuration changes.
func (mod *osloginModule) metadataSubscriber(ctx context.Context, evType string, data any, evData *events.EventData) (bool, bool, error) {
	desc, ok := evData.Data.(*metadata.Descriptor)

	// If the event manager is passing a non expected data type we log it and
	// don't renew the handler.
	if !ok {
		return false, true, fmt.Errorf("event's data is not a metadata descriptor: %+v", evData.Data)
	}

	// If the event manager is passing/reporting an error we log it and keep
	// renewing the handler.
	if evData.Error != nil {
		return true, true, fmt.Errorf("metadata event watcher reported error: %v, will retry setup", evData.Error)
	}

	return mod.osloginSetup(ctx, desc)
}

// setupSles16OSLoginDirs copies files from /usr to /etc for SLES 16. This is
// only needed for OSLogin required files that are installed in /usr but not
// /etc on SLES 16.
// https://documentation.suse.com/releasenotes/sles/html/releasenotes_sles_16.0/index.html#jsc-PED-4963
func (mod *osloginModule) setupSles16OSLoginDirs(ctx context.Context) error {
	if mod.sles16Setup.Load() {
		galog.V(2).Debugf("Skipping OSLogin SLES 16 specific setup")
		return nil
	}

	info := osinfoRead()

	if !strings.Contains(info.OS, "sles") || info.Version.Major != 16 {
		galog.Infof("Skipping OSLogin SLES 16 specific setup on %s %d", info.OS, info.Version.Major)
		mod.sles16Setup.Store(true)
		return nil
	}

	for k, v := range sles16Map {
		if file.Exists(v, file.TypeFile) {
			galog.Infof("File %q already exists, skipping copy from %q", v, k)
			continue
		}

		galog.Infof("Copying file %q -> %q", k, v)
		if err := file.CopyFile(ctx, k, v, file.Options{Perm: 0644}); err != nil {
			return fmt.Errorf("failed to copy %q -> %q: %w", k, v, err)
		}
	}
	mod.sles16Setup.Store(true)
	return nil
}

// osloginSetup is the actual oslogin's configuration entry point.
func (mod *osloginModule) osloginSetup(ctx context.Context, desc *metadata.Descriptor) (bool, bool, error) {
	defer func() {
		mod.prevMetadata = desc
		mod.decrementRetryCount()
	}()

	// If the module has failed due to a permanent error, we return early and
	// don't retry.
	if mod.permanentFailure.Load() {
		return true, true, nil
	}

	// If the we are not forcing a retry and the metadata has not changed, we
	// return early.
	//
	// The only case where we are forcefully retrying is if a systemd unit/service
	// is required to be restarted and has previously failed.
	//
	// We don't need to clean up the files here because the textconfig library
	// rolls back its previous changes before applying new ones.
	if !mod.forcedRetrying() && !mod.metadataChanged(desc) && !mod.failedConfiguration.Load() {
		return true, true, nil
	}

	evManager := events.FetchManager()

	// If the module is disabled make sure the configuration is disabled and
	// return early.
	if !desc.OSLoginEnabled() {
		defer func() { mod.enabled.Store(false) }()

		if !mod.enabled.Load() {
			return true, true, nil
		}
		// If the module is disabled now but was previously enabled do the
		// run the disabling path.
		if err := mod.disableOSLogin(ctx, evManager); err != nil {
			// Failed to restart the necessary services.
			mod.failedConfiguration.Store(true)
			return true, false, fmt.Errorf("failed to disable OS Login: %w", err)
		}

		mod.failedConfiguration.Store(false)
		return true, false, nil
	}

	galog.Info("Enabling OS Login")

	// Enable/start the ssh trusted ca pipe event handler.
	if mod.pipeEventHandler == nil {
		mod.pipeEventHandler = newPipeEventHandler(pipeWatcherSubscriberID, metadata.New())
	}

	// Enable/start the ssh trusted ca pipe event watcher.
	if mod.pipeEventWatcher == nil {
		mod.pipeEventWatcher = pipewatcher.New(sshcaEventWatcherID, sshcaPipeWatcherOpts)
		evManager.AddWatcher(ctx, mod.pipeEventWatcher)
	}

	var failed bool
	var errs error

	// Attempt to copy files from /usr to /etc for SLES 16 first time we enable
	// OSLogin.
	if err := mod.setupSles16OSLoginDirs(ctx); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to setup OSLogin directories for SLES 16: %w", err))
		mod.enabled.Store(false)
		mod.failedConfiguration.Store(true)
		// Don't attempt to continue the setup if this fails, return early. Expected
		// files don't exist, half setup could leave the system in a bad state.
		return true, false, errs
	}

	// Write SSH config.
	if err := mod.setupOpenSSH(desc); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to setup openssh: %w", err))
		failed = true
	}

	// Write NSSwitch config.
	if err := mod.setupNSSwitch(false); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to setup nsswitch: %w", err))
		failed = true
	}

	// Write PAM config.
	if err := mod.setupPAM(desc); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to setup pam: %w", err))
		failed = true
	}

	// Write Group config.
	if err := mod.setupGroup(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to setup group: %w", err))
		failed = true
	}

	// Restart services. This is not a blocker.
	if err := mod.restartServices(ctx); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to restart services: %w", err))
		failed = true
	}

	// Create the necessary OSLogin directories and other files.
	if err := mod.setupOSLoginDirs(ctx); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to setup OSLogin directories: %w", err))
		failed = true
	}

	if err := mod.setupOSLoginSudoers(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to create OSLogin sudoers file: %w", err))
		failed = true
	}

	// Fill NSS cache.
	if _, err := run.WithContext(ctx, run.Options{
		Name:       "google_oslogin_nss_cache",
		OutputType: run.OutputNone,
	}); err != nil {
		errs = errors.Join(errs, fmt.Errorf("failed to fill NSS cache: %w", err))
		failed = true
	}

	mod.enabled.Store(!failed)
	mod.failedConfiguration.Store(failed)
	return true, false, errs
}

// setupOpenSSH configures the openssh daemon.
func (mod *osloginModule) setupOpenSSH(desc *metadata.Descriptor) error {
	galog.Debug("Configuring OpenSSH daemon for OS Login.")
	sshdCfg := textconfig.New(mod.sshdConfigPath, osloginConfigMode, osloginConfigOpts)
	block := textconfig.NewBlock(textconfig.Top)
	sshdCfg.AddBlock(block)

	// Determine the authorized keys command binary.
	authorizedKeysCommand, err := availableBinary(mod.authorizedKeysCommandPaths)
	if err != nil {
		return fmt.Errorf("failed to find authorized keys command binary: %w", err)
	}
	if desc.SecurityKeyEnabled() {
		authorizedKeysCommand, err = availableBinary(mod.authorizedKeysCommandSKPaths)
		if err != nil {
			return fmt.Errorf("failed to find authorized keys command binary: %w", err)
		}
	}

	cfg := cfg.Retrieve()
	certReq := desc.CertRequiredEnabled()
	if certReq || cfg.OSLogin.CertAuthentication {
		// Add the relevant certificate authority keys.
		block.Append("TrustedUserCAKeys", defaultPipePath)
		block.Append("AuthorizedPrincipalsCommand", "/usr/bin/google_authorized_principals %u %k")
		block.Append("AuthorizedPrincipalsCommandUser", "root")
	}
	if !certReq && cfg.OSLogin.CertAuthentication {
		block.Append("AuthorizedKeysCommand", authorizedKeysCommand)
		block.Append("AuthorizedKeysCommandUser", "root")
	}

	bottomBlock := textconfig.NewBlock(textconfig.Bottom)
	// Source per-user config from /var/google-users.d.
	bottomBlock.Append("Include", "/var/google-users.d/*")
	sshdCfg.AddBlock(bottomBlock)

	// Add two-factor authentication configuration if enabled.
	if desc.TwoFactorEnabled() {
		block.Append("AuthenticationMethods", "publickey,keyboard-interactive")
		block.Append("ChallengeResponseAuthentication", "yes")

		bottomBlock.Append("Match", "User sa_*")
		bottomBlock.Append("AuthenticationMethods", "publickey")
	}

	if err := sshdCfg.Apply(); err != nil {
		return fmt.Errorf("failed to apply openssh config: %w", err)
	}
	galog.Debug("Successfully configured OpenSSH daemon for OS Login.")
	return nil
}

// setupNSSwitch configures the NSSwitch configuration file. If cleanup is true
// then the configuration is rolled back, otherwise it is set up.
func (mod *osloginModule) setupNSSwitch(cleanup bool) error {
	logMessage := "Setting up"
	if cleanup {
		logMessage = "Rolling back"
	}
	galog.Debugf("%s NSSwitch configurations for OS Login", logMessage)
	nsswitch, err := os.ReadFile(mod.nsswitchConfigPath)
	if err != nil {
		return fmt.Errorf("failed to read nsswitch.conf: %w", err)
	}

	var lines []string
	for _, line := range strings.Split(string(nsswitch), "\n") {
		if strings.HasPrefix(line, "passwd:") || strings.HasPrefix(line, "group:") {
			if cleanup {
				line = strings.Replace(line, "cache_oslogin oslogin", "", 1)
				line = strings.TrimSpace(line)
			} else {
				if !strings.Contains(line, "oslogin") {
					line += " cache_oslogin oslogin"
				}
			}
		}
		lines = append(lines, line)
	}

	if err := os.WriteFile(mod.nsswitchConfigPath, []byte(strings.Join(lines, "\n")), osloginConfigMode); err != nil {
		return fmt.Errorf("failed to write nsswitch.conf: %w", err)
	}
	galog.Debugf("Finished %s OS Login nsswitch configuration.", strings.ToLower(logMessage))
	return nil
}

// setupPAM configures the PAM module.
func (mod *osloginModule) setupPAM(desc *metadata.Descriptor) error {
	galog.Debug("Configuring PAM module for OS Login.")

	// Make sure the PAM config directory exists, we have seen custom images not
	// having this directory, since it's a mandatory configuration we must ensure
	// it exists and the configuration can be written.
	pamConfigDir := filepath.Dir(mod.pamConfigPath)
	if !file.Exists(pamConfigDir, file.TypeDir) {
		if err := os.Mkdir(pamConfigDir, 0755); err != nil {
			return fmt.Errorf("failed to create PAM config directory: %w", err)
		}
	}

	pamConfig := textconfig.New(mod.pamConfigPath, osloginConfigMode, osloginConfigOpts)
	topBlock := textconfig.NewBlock(textconfig.Top)
	bottomBlock := textconfig.NewBlock(textconfig.Bottom)
	pamConfig.AddBlock(topBlock)
	pamConfig.AddBlock(bottomBlock)

	pamOSLogin := "[success=done perm_denied=die default=ignore]"
	pamGroup := "[default=ignore]"
	session := "[success=ok default=ignore]"

	if desc.TwoFactorEnabled() {
		topBlock.Append("auth", fmt.Sprintf("%s pam_oslogin_login.so", pamOSLogin))
	}
	topBlock.Append("auth", fmt.Sprintf("%s pam_group.so", pamGroup))
	bottomBlock.Append("session", fmt.Sprintf("%s pam_mkhomedir.so", session))

	if err := pamConfig.Apply(); err != nil {
		return fmt.Errorf("failed to apply pam config: %w", err)
	}
	galog.Debug("Successfully configured PAM module for OS Login.")
	return nil
}

// setupGroup configures the group config.
func (mod *osloginModule) setupGroup() error {
	galog.Debug("Setting up OS Login group configuration.")
	groupConf := textconfig.New(mod.groupConfigPath, osloginConfigMode, osloginConfigOpts)

	block := textconfig.NewBlock(textconfig.Bottom)
	groupConf.AddBlock(block)

	config := "sshd;*;*;Al0000-2400;video"
	block.Append(config, "")

	if err := groupConf.Apply(); err != nil {
		return fmt.Errorf("failed to apply group config: %w", err)
	}
	galog.Debug("Successfully set up OS Login group configuration.")
	return nil
}

// availableBinary returns the first binary in the fpath that exists.
func availableBinary(fpath []string) (string, error) {
	for _, f := range fpath {
		if file.Exists(f, file.TypeFile) {
			return f, nil
		}
	}
	return "", fmt.Errorf("no binary found in %v", fpath)
}

// removeDeprecatedEntries removes the deprecated entries from the files.
func (mod *osloginModule) removeDeprecatedEntries() error {
	// Clean up the deprecated files.
	for f, entries := range mod.deprecatedEntries {
		deprecatedEntryOpts := textconfig.Options{
			Delimiters: &textconfig.Delimiter{
				Start: "#### Google OS Login control. Do not edit this section. ####",
				End:   "#### End Google OS Login control section. ####",
			},
			DeprecatedEntries: entries,
		}
		handle := textconfig.New(f, osloginConfigMode, deprecatedEntryOpts)
		if err := handle.Cleanup(); err != nil {
			return fmt.Errorf("failed to cleanup deprecated file %s: %w", f, err)
		}
	}
	galog.Debug("Removed deprecated entries from OS Login configuration.")
	return nil
}

// disableOSLogin stop internal "services" and rollback user's configuration.
func (mod *osloginModule) disableOSLogin(ctx context.Context, evManager *events.Manager) error {
	galog.Infof("Disabling OS Login.")
	// Make sure we only stop responding to requests to the ssh trusted ca pipe
	// when all user's configuration is rolled back.
	defer func() {
		mod.pipeEventHandler.Close()
		mod.pipeEventHandler = nil

		evManager.RemoveWatcher(ctx, mod.pipeEventWatcher)
		mod.pipeEventWatcher = nil
	}()

	// Rollback all the configuration.
	galog.Debugf("Rolling back OpenSSH config %s", mod.sshdConfigPath)
	sshdCfg := textconfig.New(mod.sshdConfigPath, osloginConfigMode, osloginConfigOpts)
	if err := sshdCfg.Cleanup(); err != nil {
		return fmt.Errorf("failed to rollback openssh config: %w", err)
	}
	galog.Debugf("Successfully rolled back OpenSSH config %s", mod.sshdConfigPath)

	if err := mod.setupNSSwitch(true); err != nil {
		return fmt.Errorf("failed to rollback nsswitch config: %w", err)
	}

	galog.Debugf("Rolling back PAM config %s", mod.pamConfigPath)
	pamCfg := textconfig.New(mod.pamConfigPath, osloginConfigMode, osloginConfigOpts)
	if err := pamCfg.Cleanup(); err != nil {
		return fmt.Errorf("failed to rollback pam config: %w", err)
	}
	galog.Debugf("Successfully rolled back PAM config %s", mod.pamConfigPath)

	galog.Debugf("Rolling back group config %s", mod.groupConfigPath)
	groupCfg := textconfig.New(mod.groupConfigPath, osloginConfigMode, osloginConfigOpts)
	if err := groupCfg.Cleanup(); err != nil {
		return fmt.Errorf("failed to rollback group config: %w", err)
	}
	galog.Debugf("Successfully rolled back group config %s", mod.groupConfigPath)

	// Restart the services to reflect the rollback.
	if err := mod.restartServices(ctx); err != nil {
		return fmt.Errorf("failed to restart services: %w", err)
	}

	return nil
}

// setupOSLoginDirs creates the necessary directories for OSLogin, if necessary.
// This also runs restorecon on the new directories.
func (mod *osloginModule) setupOSLoginDirs(ctx context.Context) error {
	restorecon, restoreconerr := execLookPath("restorecon")

	for _, dir := range mod.osloginDirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("failed to create oslogin directory: %w", err)
		}
		if restoreconerr != nil {
			continue
		}
		if _, err := run.WithContext(ctx, run.Options{
			OutputType: run.OutputNone,
			Name:       restorecon,
			Args:       []string{dir},
		}); err != nil {
			return fmt.Errorf("failed to run restorecon on %s: %w", dir, err)
		}
	}
	return nil
}

// setupOSLoginSudoers creates the necessary sudoers file for OSLogin,
// if necessary.
func (mod *osloginModule) setupOSLoginSudoers() error {
	if file.Exists(mod.sudoers, file.TypeFile) {
		galog.Debugf("Sudoers file %s already exists, skipping.", mod.sudoers)
		return nil
	}
	if err := os.WriteFile(mod.sudoers, []byte("#includedir /var/google-sudoers.d\n"), 0440); err != nil {
		return fmt.Errorf("failed to write sudoers file: %w", err)
	}
	galog.Debugf("Wrote OS Login sudoers file %s", mod.sudoers)
	return nil
}

// metadataChanged returns true if the metadata has changed or if it's being
// called on behalf of the first handler's execution.
func (mod *osloginModule) metadataChanged(desc *metadata.Descriptor) bool {
	// If the module has not been initialized yet then we return true to force
	// the first execution of the setup.
	if mod.prevMetadata == nil {
		return true
	}

	// Have the metadata's oslogin knobs changed?
	if desc.OSLoginEnabled() != mod.prevMetadata.OSLoginEnabled() {
		return true
	}

	// Have the metadata's two factor authentication knobs changed?
	if desc.TwoFactorEnabled() != mod.prevMetadata.TwoFactorEnabled() {
		return true
	}

	// Has the metadata's security key knobs changed?
	if desc.SecurityKeyEnabled() != mod.prevMetadata.SecurityKeyEnabled() {
		return true
	}

	// Have the metadata's cert required knobs changed?
	if desc.CertRequiredEnabled() != mod.prevMetadata.CertRequiredEnabled() {
		return true
	}

	// No changes detected.
	return false
}

// forcedRetrying returns true if the module should retry setup even if the
// metadata has not changed. Cases where this is true are when the module is
// failing to restart dependent systemd units/services.
func (mod *osloginModule) forcedRetrying() bool {
	if mod.retryCount.Load() > 0 {
		return true
	}
	return false
}

// decrementRetryCount decrements the retry count by 1.
func (mod *osloginModule) decrementRetryCount() {
	if mod.retryCount.Load() == 0 {
		return
	}
	mod.retryCount.Store(mod.retryCount.Load() - 1)
}

// restartServices restarts the provided services with the provided methods.
func (mod *osloginModule) restartServices(ctx context.Context) error {
	for method, serviceConfigs := range mod.services {
		// One of the services in each service list must be successfully restarted.
		for _, serviceConfig := range serviceConfigs {
			// Indicates if one of the services in the list was successfully
			// restarted.
			var passed bool
			var errs error
			for _, service := range serviceConfig.services {
				galog.V(2).Debugf("Checking if service %s exists", service)
				if found, err := daemon.CheckUnitExists(ctx, service); !found {
					if err != nil {
						galog.V(2).Debugf("Failed to check if service %s exists: %v", service, err)
						errs = errors.Join(errs, fmt.Errorf("failed to check if service %s exists: %w", service, err))
					}
					continue
				}

				galog.V(2).Debugf("Service %s exists, restarting...", service)
				if err := daemon.RestartService(ctx, service, method); err != nil {
					galog.V(2).Debugf("Failed to restart service %s: %v", service, err)
					errs = errors.Join(errs, fmt.Errorf("failed to restart service %s: %w", service, err))
					continue
				}
				galog.V(2).Debugf("Successfully restarted service %s", service)
				passed = true
				break
			}
			if !passed {
				if serviceConfig.protocol == serviceRestartAtLeastOne {
					if mod.retryCount.Load() > 1 {
						galog.V(2).Warnf("Failed to restart one of %v, it will be retried", serviceConfig.services)
						return nil
					}

					// We are on last attempt and failed - mark it as permanent failure.
					if mod.retryCount.Load() == 1 {
						mod.permanentFailure.Store(true)
					}

					return errors.Join(fmt.Errorf("failed to restart one of: %v", serviceConfig.services), errs)
				}
				// Only log a debug message if the restart is optional.
				galog.Debugf("Failed to restart optional services: %v", serviceConfig.services)
			}
		}
	}
	return nil
}
