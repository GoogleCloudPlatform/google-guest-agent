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

package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/GoogleCloudPlatform/galog"
)

var (
	// ErrCommandTemplate is the error returned when a CommandSpec's Command
	// template is boggus.
	ErrCommandTemplate = errors.New("invalid command format template")

	// ErrTemplateError is the error returned when a CommandSpec's Error template
	// is boggus.
	ErrTemplateError = errors.New("invalid error format template")
)

// CommandSpec defines a Command template and an Error template. The data
// structure to be used with the templates is up to the user to define.
type CommandSpec struct {
	// Command is the command template i.e: "echo '{{.MyDataString}}'".
	Command string

	// Error is the error template, if the command fails this template is
	// used to build the error message, i.e: "failed to parse file {{.FileName}}".
	Error string
}

// CommandSet is set of commands to be executed together, IOW a command batch.
type CommandSet []CommandSpec

// WithContext runs all the commands in a CommandSet, no command output is
// handled. All commands are run as a batch.
func (s CommandSet) WithContext(ctx context.Context, data any) error {
	for _, curr := range s {
		if err := curr.WithContext(ctx, data); err != nil {
			return err
		}
	}
	return nil
}

// commandFormat formats the CommandSpec's Command field. The data is passed in
// to the template parsing and execution.
func (c CommandSpec) commandFormat(data any) ([]string, error) {
	if len(strings.Trim(c.Command, " ")) == 0 {
		return nil, ErrCommandTemplate
	}

	tmpl, err := template.New("").Parse(c.Command)
	if err != nil {
		galog.Debugf("Error parsing command format: %v", err)
		return nil, ErrCommandTemplate
	}

	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, data); err != nil {
		galog.Debugf("Error executing command format: %v", err)
		return nil, ErrCommandTemplate
	}

	return strings.Split(buffer.String(), " "), nil
}

// errorFormat formats the CommandSpec's Error field. The data is passed in to
// the template parsing and execution.
func (c CommandSpec) errorFormat(data any) (string, error) {
	tmpl, err := template.New("").Parse(c.Error)
	if err != nil {
		galog.Debugf("Error parsing error format: %v", err)
		return "", ErrTemplateError
	}

	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, data); err != nil {
		galog.Debugf("Error executing error format: %v", err)
		return "", ErrTemplateError
	}

	return buffer.String(), nil
}

// WithContext runs a CommandSpec command, no command output is handled.
func (c CommandSpec) WithContext(ctx context.Context, data any) error {
	tokens, err := c.commandFormat(data)
	if err != nil {
		return err
	}

	errorMsg, err := c.errorFormat(data)
	if err != nil {
		return err
	}

	opts := Options{
		Name:       tokens[0],
		Args:       tokens[1:],
		OutputType: OutputNone,
	}

	if _, err := Client.WithContext(ctx, opts); err != nil {
		return fmt.Errorf("%w; %s: %d", err, errorMsg, len(tokens))
	}

	return nil
}
