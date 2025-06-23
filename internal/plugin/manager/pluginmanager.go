//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package manager

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	acpb "github.com/GoogleCloudPlatform/google-guest-agent/internal/acp/proto/google_guest_agent/acp"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/acs/client"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/boundedlist"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/ps"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/scheduler"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"google.golang.org/protobuf/proto"
	tpb "google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// agentStateDir is where all agent state is stored. This includes plugin
	// information and other information agent might want to store.
	agentStateDir = "agent_state"
	// pluginInfoDir is where all plugin information is stored within agent state.
	pluginInfoDir = "plugin_info"
	// healthCheckFrequency is the frequency at which plugin health check is
	// executed.
	healthCheckFrequency = 10 * time.Second
	// metricsCheckFrequency is the default frequency at which plugin metrics
	// check is executed.
	metricsCheckFrequency = 10 * time.Second
	// maxMetricDatapoints is the maximum number of datapoints to be stored in the
	// metric list in memory.
	maxMetricDatapoints = 60
	// CorePluginName is the name of the guest agent core plugin.
	CorePluginName = "GuestAgentCorePlugin"
)

// pluginManager is the instance of plugin manager.
var pluginManager *PluginManager

// PluginManager struct represents the plugins that plugin manager manages.
type PluginManager struct {
	// mu protects the plugins map.
	mu sync.RWMutex
	// plugins is the map of plugin name and plugin managed by plugin manager.
	plugins map[string]*Plugin
	// pluginMonitorMu protects the pluginMonitors map.
	pluginMonitorMu sync.Mutex
	// pluginMonitors is the map of plugin and plugin monitor ID monitoring
	// plugin.
	pluginMonitors map[string]string
	// pluginMetricsMu protects the pluginMetrics map.
	pluginMetricsMu sync.Mutex
	// pluginMetricsMonitors is the map of plugin and plugin metrics ID monitoring
	// plugin.
	pluginMetricsMonitors map[string]string
	// scheduler is the scheduler used by plugin manager to schedule plugins
	// monitoring.
	scheduler *scheduler.Scheduler
	// protocol is the protocol used by plugin manager to communicate with all
	// plugins.
	protocol string

	// pendingPluginRevisionsMu protects the pendingPluginRevisions map.
	pendingPluginRevisionsMu sync.RWMutex
	// inProgressPluginRequests keep tracks of the pending plugin requests. This
	// allows us to ignore new requests if previous request is still in progress
	// for the same plugin.
	inProgressPluginRequests map[string]bool

	// requestCountMu protects the requestCount map.
	requestCountMu sync.Mutex
	// requestCount keeps track of the number of requests received for each
	// action and also the number of successful and failures requests.
	requestCount map[acpb.ConfigurePluginStates_Action]map[bool]int

	// instanceIDMu protects the instanceID.
	instanceIDMu sync.Mutex
	// instanceID is the instance ID of the VM plugin manager is running on.
	instanceID string

	// IsInitialized indicates if plugin manager is initialized.
	IsInitialized atomic.Bool
}

// agentPluginState returns the path to the directory where agent maintains
// plugin state.
func agentPluginState() string {
	return filepath.Join(baseState(), agentStateDir, pluginInfoDir)
}

// Instance returns the previously initialized instance of plugin manager.
func Instance() *PluginManager {
	return pluginManager
}

func init() {
	pluginManager = &PluginManager{
		protocol:                 tcpProtocol,
		pluginMonitors:           make(map[string]string),
		pluginMetricsMonitors:    make(map[string]string),
		scheduler:                scheduler.Instance(),
		inProgressPluginRequests: make(map[string]bool),
		requestCount:             make(map[acpb.ConfigurePluginStates_Action]map[bool]int),
	}
}

func (m *PluginManager) cleanupOldState(ctx context.Context, path string) error {
	re := regexp.MustCompile("^[0-9]+$")

	if !file.Exists(path, file.TypeDir) {
		// This is not an error, it just means there's nothing to clean up, which
		// can happen if the agent is started for the first time or plugins were
		// never installed.
		galog.Debugf("Plugin state directory %q does not exist, skipping cleanup", path)
		return nil
	}

	dirs, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("failed to read directory %q: %w", path, err)
	}

	currentID := m.currentInstanceID()

	for _, dir := range dirs {
		absPath := filepath.Join(path, dir.Name())
		// Skip the current instance directory and any non-numeric directories,
		// these are most likely not agent created.
		if !dir.IsDir() || dir.Name() == currentID || !re.MatchString(dir.Name()) {
			galog.V(2).Debugf("Skipping %q from plugin manager old state cleanup", absPath)
			continue
		}
		galog.Debugf("Removing previous plugin state %q", absPath)
		if err := os.RemoveAll(absPath); err != nil {
			return fmt.Errorf("failed to remove file %q: %w", absPath, err)
		}
	}
	return nil
}

func (m *PluginManager) setInstanceID(id string) {
	m.instanceIDMu.Lock()
	defer m.instanceIDMu.Unlock()
	m.instanceID = id
}

func (m *PluginManager) currentInstanceID() string {
	m.instanceIDMu.Lock()
	defer m.instanceIDMu.Unlock()
	return m.instanceID
}

// InitAdHocPluginManager initializes and returns a PluginManager instance for
// ad-hoc requests that does not go through ACS. This is generally used by other
// local processes to temporarily leverage plugin manager functionality. Use
// this instead of InitPluginManager if there's no active plugin management
// required. InitAdHocPluginManager skips some initialization steps like
// scheduling plugin monitors that are not required for ad-hoc requests.
func InitAdHocPluginManager(ctx context.Context, instanceID string) (*PluginManager, error) {
	galog.Infof("Initializing ad-hoc plugin manager for instance %q", instanceID)

	pluginManager.setInstanceID(instanceID)
	plugins, err := load(agentPluginState())
	if err != nil {
		return nil, fmt.Errorf("unable to load existing plugin state: %w", err)
	}
	pluginManager.plugins = plugins

	return pluginManager, nil
}

// StopPlugin stops the plugin. This is used for ad-hoc requests that does not
// go through ACS. In case the plugin is not found or is not running, it returns
// nil error and is a no-op.
func (m *PluginManager) StopPlugin(ctx context.Context, name string) error {
	galog.Infof("Stopping plugin %q", name)

	plugin, err := m.Fetch(name)
	if err != nil {
		galog.Infof("Plugin %q state not found [err: %v], skipping stop request", name, err)
		return nil
	}

	if err := plugin.Connect(ctx); err != nil {
		galog.Infof("Failed to connect to plugin %q [err: %v], skipping graceful stop", plugin.FullName(), err)
	}

	return m.stopAndRemovePlugin(ctx, plugin)
}

// InitPluginManager initializes and returns a PluginManager instance.
// Plugin Manager can be initialized and used to support core plugins even if
// ACS is disabled. Plugin Manager will be initialized during early Guest Agent
// startup to configure the core plugins.
func InitPluginManager(ctx context.Context, instanceID string) (*PluginManager, error) {
	galog.Infof("Initializing plugin manager for instance %q", instanceID)
	pluginManager.setInstanceID(instanceID)

	// Cleanup old plugin state in a separate goroutine. This operation is not
	// critical for plugin manager initialization and should not block it.
	go func() {
		if err := pluginManager.cleanupOldState(ctx, filepath.Dir(baseState())); err != nil {
			galog.Errorf("Failed to cleanup old plugin state: %v", err)
		}
	}()

	plugins, err := load(agentPluginState())
	if err != nil {
		return nil, fmt.Errorf("unable to load existing plugin state: %w", err)
	}

	pluginManager.plugins = plugins

	if err := RegisterCmdHandler(ctx); err != nil {
		return nil, fmt.Errorf("failed to register plugin command handler: %w", err)
	}

	wg := sync.WaitGroup{}
	pluginManager.pendingPluginRevisionsMu.Lock()

	for _, p := range plugins {
		pluginManager.inProgressPluginRequests[p.Name] = true
		wg.Add(1)

		go func(p *Plugin) {
			// Regardless of the outcome, we should remove the plugin from the pending
			// list as request is no longer in process for this plugin.
			defer func() {
				pluginManager.pendingPluginRevisionsMu.Lock()
				defer pluginManager.pendingPluginRevisionsMu.Unlock()
				delete(pluginManager.inProgressPluginRequests, p.Name)
				wg.Done()
			}()

			if err := connectOrReLaunch(ctx, p); err != nil {
				galog.Errorf("Failed to connect or relaunch plugin %q: %v", p.FullName(), err)
			} else {
				pluginManager.startPluginSchedulers(ctx, p)
			}
		}(p)
	}
	pluginManager.IsInitialized.Store(true)
	pluginManager.pendingPluginRevisionsMu.Unlock()
	wg.Wait()

	if isUDSSupported() {
		pluginManager.protocol = udsProtocol
	}
	return pluginManager, nil
}

// RemoveAllDynamicPlugins filters out core plugins and triggers plugin manager
// to remove all dynamic plugins on the host. It also removes the base state
// directory to ensure that the entire state is cleaned up.
func (m *PluginManager) RemoveAllDynamicPlugins(ctx context.Context) error {
	var reqs []*acpb.ConfigurePluginStates_ConfigurePlugin
	galog.Infof("Removing all dynamic plugins")

	var toRemove []*Plugin
	for _, p := range m.list() {
		if p.PluginType == PluginTypeCore {
			galog.Debugf("Skipping core plugin %q, it will be removed by package manager", p.Name)
			continue
		}
		toRemove = append(toRemove, p)
	}

	errChan := make(chan error, len(reqs))
	wg := sync.WaitGroup{}

	for _, plugin := range toRemove {
		wg.Add(1)
		go func(p *Plugin) {
			var err error
			defer wg.Done()

			if err = m.StopPlugin(ctx, p.Name); err != nil {
				galog.Infof("Remove plugin %q completed with error: [%v]", p.FullName(), err)
				errChan <- err
			}

		}(plugin)
	}

	go func() {
		wg.Wait()
		baseStateDir := baseState()
		if file.Exists(baseStateDir, file.TypeDir) {
			if err := os.RemoveAll(baseStateDir); err != nil {
				errChan <- fmt.Errorf("failed to remove file %q: %w", baseStateDir, err)
			}
		}
		close(errChan)
	}()

	var errs error
	for err := range errChan {
		errs = errors.Join(errs, err)
	}

	return errs
}

// ListPluginStates returns the plugin states and cached health check
// information.
func (m *PluginManager) ListPluginStates(ctx context.Context, req *acpb.ListPluginStates) *acpb.CurrentPluginStates {
	galog.Debugf("Handling list plugin state request: %+v", req)
	var states []*acpb.CurrentPluginStates_DaemonPluginState
	plugins := m.list()

	for _, p := range plugins {
		status := &acpb.CurrentPluginStates_DaemonPluginState_Status{Status: p.State()}
		h := p.healthInfo()
		if h != nil {
			status.ResponseCode = h.responseCode
			status.Results = h.messages
			status.UpdateTime = tpb.New(h.timestamp)
		}

		p.RuntimeInfo.metricsMu.Lock()
		var pluginMetrics []*acpb.CurrentPluginStates_DaemonPluginState_Metric
		for _, metric := range p.RuntimeInfo.metrics.All() {
			monitorMetric := &acpb.CurrentPluginStates_DaemonPluginState_Metric{
				Timestamp:   metric.timestamp,
				CpuUsage:    metric.cpuUsage,
				MemoryUsage: metric.memoryUsage,
			}
			pluginMetrics = append(pluginMetrics, monitorMetric)
		}

		state := &acpb.CurrentPluginStates_DaemonPluginState{
			Name:                 p.Name,
			CurrentRevisionId:    p.Revision,
			CurrentPluginStatus:  status,
			CurrentPluginMetrics: pluginMetrics,
		}

		pendingStatus := p.pendingStatus()
		if pendingStatus != nil {
			state.PendingRevisionId = pendingStatus.revision
		}

		// Flush the metrics array.
		p.RuntimeInfo.metrics.Reset()

		// Release the metrics lock.
		p.RuntimeInfo.metricsMu.Unlock()

		// Append the state to the list.
		states = append(states, state)
	}

	return &acpb.CurrentPluginStates{DaemonPluginStates: states}
}

// ConfigurePluginStates configures the plugin states as stated in the request.
// localPlugin identifies if the plugin is a core plugin. These core plugins are
// installed by package managers but not launched along with Guest Agent binary.
// Plugin Manager will launch and manage lifecycle of core plugins along with
// other dynamic plugins.
func (m *PluginManager) ConfigurePluginStates(ctx context.Context, req *acpb.ConfigurePluginStates, localPlugin bool) {
	galog.Debugf("Handling configure plugin state request: %+v, local plugin: %t", req, localPlugin)
	wg := sync.WaitGroup{}

	var toProcess []*acpb.ConfigurePluginStates_ConfigurePlugin

	m.pendingPluginRevisionsMu.Lock()
	for _, req := range req.GetConfigurePlugins() {
		_, inProgress := m.inProgressPluginRequests[req.GetPlugin().GetName()]
		if inProgress {
			// There's already a request in progress for this plugin, ignore this
			// request.
			galog.Infof("Ignoring request to %v plugin %q, another request already in progress", req.GetAction(), req.GetPlugin().GetName())
			continue
		}
		toProcess = append(toProcess, req)
		m.inProgressPluginRequests[req.GetPlugin().GetName()] = true
	}
	m.pendingPluginRevisionsMu.Unlock()

	for _, req := range toProcess {
		wg.Add(1)
		go func(req *acpb.ConfigurePluginStates_ConfigurePlugin) {
			defer wg.Done()

			// Regardless of the outcome, we should remove the plugin from the pending
			// list as request is no longer in process for this plugin.
			defer func() {
				m.pendingPluginRevisionsMu.Lock()
				defer m.pendingPluginRevisionsMu.Unlock()
				delete(m.inProgressPluginRequests, req.GetPlugin().GetName())
			}()

			m.configurePlugin(ctx, req, localPlugin)
		}(req)
	}

	wg.Wait()
	galog.Debugf("Configure plugin state request completed")
}

// list returns the list of currently managed plugins.
func (m *PluginManager) list() []*Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var plugins []*Plugin
	for _, p := range m.plugins {
		plugins = append(plugins, p)
	}
	return plugins
}

// Fetch returns the plugin instance with the given name.
func (m *PluginManager) Fetch(name string) (*Plugin, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.plugins[name]
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", name)
	}
	return p, nil
}

// add stores the plugin instance with the given name.
func (m *PluginManager) add(p *Plugin) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plugins[p.Name] = p
}

// delete deletes the plugin instance with the given name.
func (m *PluginManager) delete(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.plugins, name)
}

func (m *PluginManager) configurePlugin(ctx context.Context, req *acpb.ConfigurePluginStates_ConfigurePlugin, localPlugin bool) {
	var success bool
	switch req.GetAction() {
	case acpb.ConfigurePluginStates_INSTALL:
		if err := m.installPlugin(ctx, req, localPlugin); err != nil {
			galog.Errorf("Failed to install plugin %q, revision %q: %v", req.GetPlugin().GetName(), req.GetPlugin().GetRevisionId(), err)
		} else {
			success = true
		}
	case acpb.ConfigurePluginStates_REMOVE:
		if err := m.removePlugin(ctx, req); err != nil {
			galog.Errorf("Failed to remove plugin %q, revision %q: %v", req.GetPlugin().GetName(), req.GetPlugin().GetRevisionId(), err)
		} else {
			success = true
		}
	default:
		galog.Warnf("Unknown action (%s) for configure plugin state request, ignoring", req.GetAction().String())
	}

	m.requestCountMu.Lock()
	defer m.requestCountMu.Unlock()
	if m.requestCount[req.GetAction()] == nil {
		m.requestCount[req.GetAction()] = make(map[bool]int)
	}
	m.requestCount[req.GetAction()][success]++
}

// setMetricConfig sets the default metric configurations for the plugin or
// overrides it from request proto if provided.
func (p *Plugin) setMetricConfig(req *acpb.ConfigurePluginStates_ConfigurePlugin) {
	p.Manifest.MetricsInterval = metricsCheckFrequency
	p.Manifest.MaxMetricDatapoints = maxMetricDatapoints

	if points := req.GetManifest().GetMaxMetricDatapoints(); points != 0 {
		p.Manifest.MaxMetricDatapoints = uint(points)
	}

	if interval := req.GetManifest().GetMetricsInterval().GetSeconds(); interval != 0 {
		p.Manifest.MetricsInterval = time.Duration(interval) * time.Second
	}

	p.RuntimeInfo.metrics = boundedlist.New[Metric](p.Manifest.MaxMetricDatapoints)
}

// newPluginManifest generates agent representation of the manifest from the
// install request.
func newPluginManifest(req *acpb.ConfigurePluginStates_ConfigurePlugin) (*Manifest, error) {
	manifest := &Manifest{
		StartAttempts:  int(req.GetManifest().GetStartAttemptCount()),
		MaxMemoryUsage: req.GetManifest().GetMaxMemoryUsageBytes(),
		MaxCPUUsage:    req.GetManifest().GetMaxCpuUsagePercentage(),
		StopTimeout:    time.Duration(req.GetManifest().GetStopTimeout().GetSeconds()) * time.Second,
		StartTimeout:   time.Duration(req.GetManifest().GetStartTimeout().GetSeconds()) * time.Second,
		StartConfig:    &ServiceConfig{},
	}

	if req.GetManifest().Config == nil {
		return manifest, nil
	}

	switch req.GetManifest().Config.(type) {
	case *acpb.ConfigurePluginStates_Manifest_StringConfig:
		manifest.StartConfig.Simple = req.GetManifest().GetStringConfig()
	case *acpb.ConfigurePluginStates_Manifest_StructConfig:
		// Marshal the service config to a byte array to persist it on disk. This
		// will be un-marshaled to use at plugin launch time.
		bytes, err := proto.Marshal(req.GetManifest().GetStructConfig())
		if err != nil {
			return nil, fmt.Errorf("unable to marshal service config: %w", err)
		}
		manifest.StartConfig.Structured = bytes
	}

	return manifest, nil
}

// newPlugin creates a new plugin instance from the request.
// Rest of the plugin instance values are set at run time when install
// steps are executed on it.
func newPlugin(req *acpb.ConfigurePluginStates_ConfigurePlugin, localPlugin bool) (*Plugin, error) {
	p := &Plugin{
		Name:        req.GetPlugin().GetName(),
		PluginType:  PluginTypeDynamic,
		Revision:    req.GetPlugin().GetRevisionId(),
		RuntimeInfo: &RuntimeInfo{},
	}

	manifest, err := newPluginManifest(req)
	if err != nil {
		return nil, fmt.Errorf("unable to generate plugin service config: %w", err)
	}

	p.Manifest = manifest

	if localPlugin {
		// Dynamic plugins are installed in a specific directory, that install
		// workflow sets its install path. In case of local plugins since they're
		// already present on disk and directory is known set its install path here
		// itself.
		p.InstallPath = filepath.Dir(req.GetPlugin().GetEntryPoint())
		// Only core plugins can be present on disk before Plugin Manager installs.
		p.PluginType = PluginTypeCore
	}

	p.setMetricConfig(req)

	return p, nil
}

// installPlugin installs checks if the plugin already exists and does a
// fresh install or removes existing plugin revision and installs a new one.
func (m *PluginManager) installPlugin(ctx context.Context, req *acpb.ConfigurePluginStates_ConfigurePlugin, localPlugin bool) error {
	galog.Infof("Installing plugin %q, revision %q", req.GetPlugin().GetName(), req.GetPlugin().GetRevisionId())

	plugin, err := newPlugin(req, localPlugin)
	if err != nil {
		return fmt.Errorf("failed to create new plugin instance: %w", err)
	}

	sendEvent(ctx, plugin, acpb.PluginEventMessage_PLUGIN_CONFIG_INSTALL, "Received request to install a plugin.")
	currPlugin, err := m.Fetch(req.GetPlugin().GetName())
	if err == nil && currPlugin.Revision == req.GetPlugin().GetRevisionId() {
		sendEvent(ctx, currPlugin, acpb.PluginEventMessage_PLUGIN_INSTALL_FAILED, "Plugin is already installed or being processed.")
		return fmt.Errorf("plugin %q is already installed or being processed", currPlugin.FullName())
	}

	if currPlugin != nil {
		return m.upgradePlugin(ctx, req, localPlugin)
	}

	steps := m.generateInstallWorkflow(ctx, req, localPlugin)
	return m.runlaunchPluginSteps(ctx, plugin, steps)
}

// startPluginSchedulers starts all scheduler jobs for the plugin.
func (m *PluginManager) startPluginSchedulers(ctx context.Context, plugin *Plugin) {
	// At this point plugin is already running, run them in a separate go routine
	// to avoid blocking the main thread. These jobs are configured to start
	// immediately scheduling them synchronously would block the caller until they
	// finish first run.
	go func() {
		m.startMonitoring(ctx, plugin)
		m.startMetricsMonitoring(ctx, plugin)
	}()
}

// runlaunchPluginSteps runs the steps to launch the plugin. Steps differ for
// install and upgrade but the reaction to success/failure is the same.
func (m *PluginManager) runlaunchPluginSteps(ctx context.Context, plugin *Plugin, steps []Step) error {
	// Store the plugin in the manager as soon as we start processing so
	// [ListPluginStates] can send intermediate states as well.
	m.add(plugin)

	if err := plugin.runSteps(ctx, steps); err != nil {
		// Delete the plugin from the list in case it fails, agent will start fresh
		// if ACS requests to install the plugin again.
		m.delete(plugin.Name)
		sendEvent(ctx, plugin, acpb.PluginEventMessage_PLUGIN_INSTALL_FAILED, fmt.Sprintf("Failed to install plugin: %v", err))
		// If the installation fails, try cleaning up the process to prevent
		// potential conflicts or errors that unmanaged processes could cause.
		if err := ps.KillProcess(plugin.pid(), ps.KillModeNoWait); err != nil {
			// Just log the error, process might have crashed, already exited or not
			// successfully launched at all.
			galog.Warnf("Stop plugin %q finished with error: %v", plugin.FullName(), err)
		}
		// Return original install error.
		return fmt.Errorf("install plugin %q: %w", plugin.FullName(), err)
	}

	m.startPluginSchedulers(ctx, plugin)
	sendEvent(ctx, plugin, acpb.PluginEventMessage_PLUGIN_INSTALLED, "Successfully installed the plugin.")

	galog.Infof("Successfully installed plugin %q", plugin.FullName())

	return nil
}

// upgradePlugin handles the plugin revision upgrades. It downloads and unpacks
// the new plugin revision, stops the old plugin revision and then launches the
// new one.
func (m *PluginManager) upgradePlugin(ctx context.Context, req *acpb.ConfigurePluginStates_ConfigurePlugin, localPlugin bool) error {
	plugin, err := newPlugin(req, localPlugin)
	if err != nil {
		return fmt.Errorf("failed to create new plugin instance: %w", err)
	}

	currPlugin, err := m.Fetch(req.GetPlugin().GetName())
	if err != nil {
		return fmt.Errorf("fetching current instance for %q: %w", req.GetPlugin().GetName(), err)
	}

	// If new revision fails pre-launch steps, current plugin would keep running
	// and new revision process would be aborted. If new revision succeeds,
	// current plugin would be stopped and removed. Reset pending plugin status to
	// avoid showing plugin install in progress.
	defer currPlugin.resetPendingStatus()

	// Current plugin will be removed as soon as new plugin launch is started
	// below. Set the pending status to show new plugin revision install in
	// progress. This will be captured by [ListPluginStates] and sent to ACS.
	currPlugin.setPendingStatus(plugin.Revision, acpb.CurrentPluginStates_DaemonPluginState_INSTALLING)

	// Two plugin revisions can co-exist on the same host, but only one of them
	// can be running. Run pre-launch steps on new plugin revision to reduce
	// plugin downtime and make sure it can be launched.
	steps := m.preLaunchWorkflow(ctx, req, localPlugin)
	galog.Infof("Running pre-upgrade steps for plugin %q", plugin.FullName())
	if err := plugin.runSteps(ctx, steps); err != nil {
		sendEvent(ctx, plugin, acpb.PluginEventMessage_PLUGIN_INSTALL_FAILED, fmt.Sprintf("Failed to run pre-upgrade steps: %v", err))
		return fmt.Errorf("failed to run pre-upgrade steps: %w", err)
	}

	// Previously installed plugin revision already exists, remove before
	// installing a new one.
	galog.Infof("Stopping and removing old plugin %q", currPlugin.FullName())
	if err := m.stopAndRemovePlugin(ctx, currPlugin); err != nil {
		sendEvent(ctx, currPlugin, acpb.PluginEventMessage_PLUGIN_INSTALL_FAILED, fmt.Sprintf("Failed to remove plugin: %v", err))
		return fmt.Errorf("failed to remove plugin: %w", err)
	}

	return m.runlaunchPluginSteps(ctx, plugin, []Step{m.newLaunchStep(req, localPlugin)})
}

// stopAndRemovePlugin stops the given plugin, all of its schedulers and removes
// it from the manager.
func (m *PluginManager) stopAndRemovePlugin(ctx context.Context, p *Plugin) error {
	sendEvent(ctx, p, acpb.PluginEventMessage_PLUGIN_CONFIG_REMOVE, "Received request to remove a plugin.")

	// Stop all schedulers running on the plugin so it doesn't interfere with the
	// stop process.
	m.stopMonitoring(p)
	m.stopMetricsMonitoring(p)

	if err := p.runSteps(ctx, []Step{&stopStep{cleanup: true}}); err != nil {
		sendEvent(ctx, p, acpb.PluginEventMessage_PLUGIN_REMOVE_FAILED, fmt.Sprintf("Failed to remove plugin: %v", err))
		return fmt.Errorf("unable to remove plugin %q: %w", p.FullName(), err)
	}

	sendEvent(ctx, p, acpb.PluginEventMessage_PLUGIN_REMOVED, "Successfully removed the plugin.")
	m.delete(p.Name)

	galog.Infof("Successfully removed plugin %q", p.FullName())
	return nil
}

// removePlugin removes the plugin revision or ignores the request if plugin
// does not exist.
func (m *PluginManager) removePlugin(ctx context.Context, req *acpb.ConfigurePluginStates_ConfigurePlugin) error {
	galog.Infof("Removing plugin %q, revision %s", req.GetPlugin().GetName(), req.GetPlugin().GetRevisionId())

	p, err := m.Fetch(req.GetPlugin().GetName())
	if err != nil {
		sendEvent(ctx, &Plugin{Name: req.GetPlugin().GetName(), Revision: req.GetPlugin().GetRevisionId()}, acpb.PluginEventMessage_PLUGIN_REMOVE_FAILED, "Plugin not found.")
		return fmt.Errorf("plugin %q not found", req.GetPlugin().GetName())
	}

	if err := m.stopAndRemovePlugin(ctx, p); err != nil {
		return fmt.Errorf("failed to remove plugin %q: %w", p.FullName(), err)
	}

	// State directory should persist across plugin revisions and should be
	// removed only when plugin is explicitly removed.
	stateDir := p.stateDir()
	if !file.Exists(stateDir, file.TypeDir) {
		galog.Debugf("Plugin state directory %q does not exist, nothing to remove", stateDir)
		return nil
	}

	return os.RemoveAll(p.stateDir())
}

// startMonitoring schedules a plugin monitoring job that ensures the
// plugin is running. If plugin is found unhealthy it is restarted.
func (m *PluginManager) startMonitoring(ctx context.Context, p *Plugin) {
	galog.Infof("Starting plugin monitor job for plugin %q", p.FullName())
	pm := NewPluginMonitor(p, healthCheckFrequency)

	m.pluginMonitorMu.Lock()
	m.pluginMonitors[p.FullName()] = pm.ID()
	m.pluginMonitorMu.Unlock()

	// ScheduleJob() throws error only if we try to schedule a job that should
	// not be enabled (ShouldEnable() returns false). Ignoring this error
	// here as in case of monitoring pm.ShouldEnable() always returns true.
	m.scheduler.ScheduleJob(ctx, pm)
}

// stopMonitoring stops/removes the plugin monitoring job.
func (m *PluginManager) stopMonitoring(p *Plugin) {
	galog.Infof("Removing plugin monitor job for plugin %q", p.FullName())
	galog.Infof("Plugin monitors: %+v", m.pluginMonitors)
	pm, ok := m.pluginMonitors[p.FullName()]
	if !ok {
		galog.Warnf("Plugin monitor not found for %q, ignoring stop monitor request", p.FullName())
		return
	}
	m.scheduler.UnscheduleJob(pm)
	m.pluginMonitorMu.Lock()
	defer m.pluginMonitorMu.Unlock()
	delete(m.pluginMonitors, p.FullName())
}

// startMetricsMonitoring schedules a plugin resource metrics collection job
// that collects and stores them in memory.
func (m *PluginManager) startMetricsMonitoring(ctx context.Context, p *Plugin) {
	galog.Infof("Starting plugin metrics monitor job for plugin %q", p.FullName())
	pm := NewPluginMetrics(p, p.Manifest.MetricsInterval)
	// ScheduleJob() throws error only if we try to schedule a job that should
	// not be enabled (ShouldEnable() returns false). Ignoring this error
	// here as in case of monitoring pm.ShouldEnable() always returns true.
	m.scheduler.ScheduleJob(ctx, pm)
	m.pluginMetricsMu.Lock()
	defer m.pluginMetricsMu.Unlock()
	m.pluginMetricsMonitors[p.FullName()] = pm.ID()
}

// stopMetricsMonitoring stops/removes the plugin metrics monitoring job.
func (m *PluginManager) stopMetricsMonitoring(p *Plugin) {
	galog.Infof("Removing plugin metrics monitor job for plugin %q", p.FullName())
	galog.Infof("Plugin metrics monitors: %+v", m.pluginMetricsMonitors)

	m.pluginMetricsMu.Lock()
	pm, ok := m.pluginMetricsMonitors[p.FullName()]
	m.pluginMetricsMu.Unlock()

	if !ok {
		galog.Warnf("Plugin metrics monitor not found for %q, ignoring stop monitor request", p.FullName())
		return
	}
	m.scheduler.UnscheduleJob(pm)
	m.pluginMetricsMu.Lock()
	defer m.pluginMetricsMu.Unlock()
	delete(m.pluginMetricsMonitors, p.FullName())
}

// connectOrReLaunch connects to the plugin and launches the plugin if needed.
func connectOrReLaunch(ctx context.Context, p *Plugin) error {
	if p.IsRunning(ctx) {
		p.setState(acpb.CurrentPluginStates_DaemonPluginState_RUNNING)
		return nil
	}
	galog.Debugf("Plugin %q is not running, relaunching", p.FullName())
	if err := p.runSteps(ctx, relaunchWorkflow(ctx, p)); err != nil {
		p.setState(acpb.CurrentPluginStates_DaemonPluginState_CRASHED)
		return fmt.Errorf("failed to relaunch plugin %q: %w", p.FullName(), err)
	}

	return nil
}

// load loads the plugin information from directory and returns a map of
// plugins.
func load(stateDir string) (map[string]*Plugin, error) {
	galog.Debugf("Loading plugin state from %s", stateDir)

	plugins := make(map[string]*Plugin)

	files, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Plugin state might not exist yet to load from disk just log.
			galog.Debugf("Plugin state directory %q does not exist, nothing to load", stateDir)
			return plugins, nil
		}
		return nil, fmt.Errorf("unable to load plugin state from directory %s: %v", stateDir, err)
	}

	for _, f := range files {
		if f.IsDir() {
			galog.Debugf("Found unknown directory %q in %q, ignoring", f.Name(), stateDir)
			continue
		}
		file := filepath.Join(stateDir, f.Name())
		fh, err := os.Open(file)
		if err != nil {
			return nil, fmt.Errorf("unabled to read plugin state from %s: %w", file, err)
		}
		defer fh.Close()

		plugin := &Plugin{}
		if err := gob.NewDecoder(fh).Decode(plugin); err != nil {
			return nil, fmt.Errorf("unable to decode plugin state file %s: %w", f, err)
		}
		plugin.RuntimeInfo.metrics = boundedlist.New[Metric](plugin.Manifest.MaxMetricDatapoints)
		plugins[plugin.Name] = plugin
	}
	return plugins, nil
}

// sendEvent sends a plugin event on ACS channel.
func sendEvent(ctx context.Context, p *Plugin, evType acpb.PluginEventMessage_PluginEventType, details string) {
	event := &acpb.PluginEventMessage{
		PluginName:     p.Name,
		RevisionId:     p.Revision,
		EventType:      evType,
		EventTimestamp: tpb.New(time.Now()),
		EventDetails:   []byte(details),
	}
	// This might do a retry on the client side if it fails no point in blocking
	// the caller.
	go func() {
		if err := client.Notify(ctx, event); err != nil {
			// Just log the error, Notify() internally handles retrying the request
			// if this fails there's nothing really we can do.
			galog.Errorf("Failed to sent event notification [%+v]: %v", event, err)
		}
	}()
}
