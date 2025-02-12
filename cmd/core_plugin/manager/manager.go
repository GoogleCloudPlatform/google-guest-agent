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

// Package manager is the module manager, it wraps the initialization and
// mass notification of core-plugin's modules.
package manager

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/galog"
)

// ModuleStage is the stage in which a module should be grouped in.
type ModuleStage int

// InitType is the type of initialization a module should be executed.
type InitType int

// ModuleStatus is the status of a module initialization.
type ModuleStatus int

const (
	// EarlyStage represents the early stage of core-plugin execution, modules in
	// such stage are executed on behalf of early platform initialization, it's
	// assumed that the OS is still not yet fully compatible/configured with GCE
	// platform.
	EarlyStage ModuleStage = iota
	// LateStage represents the late(r) stage of core-plugin execution, it takes
	// off after the early stage has finished and we've notified Guest Agent.
	LateStage

	// BlockingInit represents the module initialization executed in a blocking
	// manner.
	BlockingInit InitType = iota
	// ConcurrentInit represents the module initialization executed in a
	// concurrent manner.
	ConcurrentInit

	// StatusSkipped represents a module initialization that was skipped.
	StatusSkipped ModuleStatus = iota
	// StatusFailed represents a module initialization that failed.
	StatusFailed
	// StatusSucceeded represents a module initialization that succeeded.
	StatusSucceeded
)

var (
	// modManager is the module manager instance.
	modManager = moduleManager{
		modules: make(map[ModuleStage][]*Module),
		metrics: make(map[string]*ModuleMetric),
	}
)

// moduleManager is the module manager's context structure.
type moduleManager struct {
	// mux is a mutex to protect the modules map.
	mux sync.Mutex
	// modules is a map of modules registered for a given stage.
	modules map[ModuleStage][]*Module
	// metrics is a map of module metrics.
	metrics map[string]*ModuleMetric
}

// ModuleMetric contains the module's initialization metrics.
type ModuleMetric struct {
	// Module is the module that was initialized.
	Module *Module
	// Stage is the stage in which the module was initialized.
	Stage ModuleStage
	// Start is the time the module initialization started.
	Start time.Time
	// End is the time the module initialization ended.
	End time.Time
	// Err is the error/status of the module initialization.
	Err error
	// Status is the status of the module initialization.
	Status ModuleStatus
	// InitType is the type of initialization the module was executed.
	InitType InitType
}

// Metrics returns/exposes the modules metrics. The returned map is a copy of
// the internal map and is safe to be modified after returned (both internally
// and externally).
func Metrics() map[string]*ModuleMetric {
	modManager.mux.Lock()
	defer modManager.mux.Unlock()

	res := make(map[string]*ModuleMetric)
	maps.Copy(res, modManager.metrics)

	return res
}

// newModuleMetric creates a new module metric.
func newModuleMetric(mod *Module, stage ModuleStage, initType InitType) *ModuleMetric {
	metric := &ModuleMetric{
		Module:   mod,
		Stage:    stage,
		Start:    time.Now(),
		InitType: initType,
	}
	modManager.metrics[mod.ID] = metric
	return metric
}

// finish records the module initialization finishing time and status.
func (mm *ModuleMetric) finish(status ModuleStatus, err error) {
	mm.End = time.Now()
	mm.Err = err
	mm.Status = status
}

// Module is the configuration structure of a module.
type Module struct {
	// Enabled is a flag to indicate if the module is enabled/disabled in the
	// local configuration.
	Enabled *bool
	// ID is a string representation of a module identification.
	ID string
	// Description is a string representation of a module description.
	Description string
	// Setup is the function to initialize the module. Modules implementing this
	// function will have its execution ran in parallel with other modules - every
	// module Setup will be executed in a different goroutine.
	Setup func(ctx context.Context, data any) error
	// BlockSetup is equivalent to Setup but the modules initialization will
	// happen in sequence, meaning, a module initialization blocks the
	// initialization of all the non-initialized modules.
	BlockSetup func(ctx context.Context, data any) error
	// Quit is the function implemented by the module to get notifications to
	// "nicely" quit, the manager will wait for these executions to finish. When
	// returned from Quit the module is communicating that it's fully done.
	Quit func(ctx context.Context)
}

// Display returns a nice string with id and description of the module and is
// used to display the module in the list of modules.
func (mod *Module) Display() string {
	desc := mod.Description
	if desc == "" {
		desc = "No description available"
	}

	ID := mod.ID
	if ID == "" {
		ID = "No ID available"
	}

	return fmt.Sprintf("%s: %s.", ID, desc)
}

// modulesLen returns the number of modules registered for a given stage.
func modulesLen(stage ModuleStage) int {
	modManager.mux.Lock()
	defer modManager.mux.Unlock()
	return len(modManager.modules[stage])
}

// Register registers modules in a execution/initialization stage. The order the
// modules are registered is honored.
func Register(mods []*Module, stage ModuleStage) {
	modManager.mux.Lock()
	defer modManager.mux.Unlock()
	for _, mod := range mods {
		if mod == nil {
			continue
		}
		if mod.Enabled != nil && *mod.Enabled == false {
			galog.Debugf("Module %q is disabled, skipping.", mod.ID)
			continue
		}
		modManager.modules[stage] = append(modManager.modules[stage], mod)
	}
}

// List returns the list of modules registered for a given stage.
func List(stage ModuleStage) []*Module {
	modManager.mux.Lock()
	defer modManager.mux.Unlock()
	return modManager.modules[stage]
}

// NotifyQuit notifies modules registered for a stage that they should nicely
// quit.
func NotifyQuit(ctx context.Context, stage ModuleStage) {
	modManager.mux.Lock()
	defer modManager.mux.Unlock()
	for _, mod := range modManager.modules[stage] {
		if mod.Quit == nil {
			galog.Debugf("Module %q has no Quit function, skipping.", mod.ID)
			continue
		}
		mod.Quit(ctx)
	}
}

// RunBlocking runs all modules in a given stage in a blocking manner. The
// selection of modules is based on the existence of the BlockSetup function
// implementation. It returns an error wrapping all errors returned by the
// modules.
func RunBlocking(ctx context.Context, stage ModuleStage, data any) error {
	modManager.mux.Lock()
	defer modManager.mux.Unlock()

	for _, mod := range modManager.modules[stage] {
		metric := newModuleMetric(mod, stage, BlockingInit)

		if mod.BlockSetup == nil {
			galog.V(2).Debugf("Module %q has no BlockSetup function, skipping.", mod.ID)
			metric.finish(StatusSkipped, nil)
			continue
		}

		if err := mod.BlockSetup(ctx, data); err != nil {
			metric.finish(StatusFailed, err)
			return fmt.Errorf("failed to initialize module(%s): %w", mod.ID, err)
		}

		metric.finish(StatusSucceeded, nil)
	}

	return nil
}

// Errors is a collection of module initialization/setup errors.
type Errors struct {
	// mu is a mutex to protect the Errors slice.
	mu sync.Mutex
	// reportedErrors is the list of module initialization errors.
	reportedErrors []*moduleError
}

// moduleError is a module initialization error.
type moduleError struct {
	// module is the module that failed to initialize.
	module *Module
	// err is the error returned by the module.
	err error
}

// Each runs a function for each module initialization error.
func (e *Errors) Each(fc func(moduleID string, err error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, err := range e.reportedErrors {
		fc(err.module.ID, err.err)
	}
}

// add adds a module initialization error to the list.
func (e *Errors) add(mod *Module, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reportedErrors = append(e.reportedErrors, &moduleError{module: mod, err: err})
}

// len returns the number of module initialization errors.
func (e *Errors) len() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.reportedErrors)
}

// join returns an error joining all module initialization errors.
func (e *Errors) join() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var errs []error
	for _, err := range e.reportedErrors {
		errs = append(errs, err.err)
	}
	return errors.Join(errs...)
}

// RunConcurrent runs all modules in a given stage in parallel. The selection of
// of modules is based on the existence of the Setup function implementation. It
// returns an Errors wrapping all errors returned by the modules.
func RunConcurrent(ctx context.Context, stage ModuleStage, data any) *Errors {
	modManager.mux.Lock()
	defer modManager.mux.Unlock()

	var wg sync.WaitGroup
	errors := &Errors{}

	for _, mod := range modManager.modules[stage] {
		metrics := newModuleMetric(mod, stage, ConcurrentInit)

		if mod.Setup == nil {
			galog.V(2).Debugf("Module %q has no Setup function, skipping.", mod.ID)
			metrics.finish(StatusSkipped, nil)
			continue
		}

		wg.Add(1)
		go func(metrics *ModuleMetric) {
			defer wg.Done()

			if err := mod.Setup(ctx, data); err != nil {
				errors.add(mod, err)
				metrics.finish(StatusFailed, err)
				return
			}

			metrics.finish(StatusSucceeded, nil)
		}(metrics)
	}

	wg.Wait()
	if errors.len() > 0 {
		return errors
	}

	return nil
}

// Shutdown shuts down the module manager.
func Shutdown() {
	modManager.mux.Lock()
	defer modManager.mux.Unlock()
	modManager.modules = make(map[ModuleStage][]*Module)
}
