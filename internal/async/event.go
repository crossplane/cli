/*
Copyright 2026 The Crossplane Authors.

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

// Package async contains infrastructure for performing asynchronous tasks in
// the CLI.
package async

// Event represents an event that happened during an asynchronous operation. It
// is used to pass information back to callers that are interested in the
// operation's progress.
type Event struct {
	// Text is a description of the event. Events with the same text represent
	// updates to the status of a single sub-operation.
	Text string
	// Status is the updated status of the sub-operation.
	Status EventStatus
}

// EventStatus represents the status of an async process.
type EventStatus string

const (
	// EventStatusStarted indicates that an operation has started.
	EventStatusStarted EventStatus = "started"
	// EventStatusSuccess indicates that an operation has completed
	// successfully.
	EventStatusSuccess EventStatus = "success"
	// EventStatusFailure indicates that an operation has failed.
	EventStatusFailure EventStatus = "failure"
)

// EventChannel is a channel for sending events. We define our own type for it
// so we can attach useful functions to it.
type EventChannel chan Event

// SendEvent sends an event to an event channel. It is a no-op if the channel is
// nil. This allows event producers to produce events unconditionally, with
// callers providing an optionally nil channel.
func (ch EventChannel) SendEvent(text string, status EventStatus) {
	if ch == nil {
		return
	}
	ch <- Event{
		Text:   text,
		Status: status,
	}
}
