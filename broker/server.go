/*
Copyright 2014 Google Inc. All Rights Reserved.

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

// Package broker implements the cloud broker.
package broker

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"

	"golang.org/x/net/context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	emulators "google/emulators"
	pb "google/protobuf"
)

var (
	EMPTY = &pb.Empty{}

	// emulator states
	OFFLINE  = "offline"
	STARTING = "starting"
	ONLINE   = "online"
)
var config *Config

type emulator struct {
	spec  *emulators.EmulatorSpec
	cmd   *exec.Cmd
	state string
}

func newEmulator(spec *emulators.EmulatorSpec) *emulator {
	return &emulator{spec: spec, state: OFFLINE}
}

func (emu *emulator) run() {
	log.Printf("Broker: Running %q", emu.spec.Id)

	err := emu.cmd.Run()
	if err != nil {
		log.Printf("Broker: Error running %q", emu.spec.Id)
	}
	log.Printf("Broker: Process returned %s", emu.cmd.ProcessState.Success)
}

func (emu *emulator) start() error {
	if emu.state != OFFLINE {
		return fmt.Errorf("Emulator %q cannot be started because it is in state %q.", emu.spec.Id, emu.state)
	}

	cmdLine := emu.spec.CommandLine
	cmd := exec.Command(cmdLine.Path, cmdLine.Args...)
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "TESTENV_BROKER_ADDRESS=localhost:10000")

	// Create stdout, stderr streams of type io.Reader
	pout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	go outputLogPrefixer(emu.spec.Id, pout)

	perr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	go outputLogPrefixer("ERR "+emu.spec.Id, perr)
	emu.cmd = cmd
	emu.state = STARTING

	go emu.run()
	return nil
}

func (emu *emulator) stop() error {
	if emu.state != STARTING || emu.state != ONLINE {
		return fmt.Errorf("Emulator %q cannot be stopped because it is in state %q.", emu.spec.Id, emu.state)
	}
	emu.cmd.Process.Signal(os.Interrupt)
	emu.state = OFFLINE
	return nil
}

type server struct {
	emulators map[string]*emulator
	mu        sync.Mutex
}

func New() *server {
	log.Printf("Broker: Server created.")
	return &server{emulators: make(map[string]*emulator)}
}

// Creates a spec to resolve targets to specified emulator endpoints.
// If a spec with this id already exists, returns ALREADY_EXISTS.
func (s *server) CreateEmulatorSpec(ctx context.Context, req *emulators.CreateEmulatorSpecRequest) (*emulators.EmulatorSpec, error) {
	log.Printf("Broker: CreateEmulatorSpec %v.", req.Spec)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.emulators[req.SpecId]
	if ok {
		return nil, grpc.Errorf(codes.AlreadyExists, "Emulator spec %q already exists.", req.SpecId)
	}

	s.emulators[req.SpecId] = newEmulator(req.Spec)
	return req.Spec, nil
}

// Finds a spec, by id. Returns NOT_FOUND if the spec doesn't exist.
func (s *server) GetEmulatorSpec(ctx context.Context, specId *emulators.SpecId) (*emulators.EmulatorSpec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	emu, ok := s.emulators[specId.Value]
	if !ok {
		return nil, grpc.Errorf(codes.NotFound, "Emulator spec %q doesn't exist.", specId.Value)
	}
	return emu.spec, nil
}

// Updates a spec, by id. Returns NOT_FOUND if the spec doesn't exist.
func (s *server) UpdateEmulatorSpec(ctx context.Context, spec *emulators.EmulatorSpec) (*emulators.EmulatorSpec, error) {
	log.Printf("Broker: UpdateEmulatorSpec %v.", spec)
	s.mu.Lock()
	defer s.mu.Unlock()
	emu, ok := s.emulators[spec.Id]
	if !ok {
		return nil, grpc.Errorf(codes.NotFound, "Emulator spec %q doesn't exist.", spec.Id)
	}
	emu.spec = spec
	return spec, nil
}

// Removes a spec, by id. Returns NOT_FOUND if the spec doesn't exist.
func (s *server) DeleteEmulatorSpec(ctx context.Context, specId *emulators.SpecId) (*pb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.emulators[specId.Value]
	if !ok {
		return nil, grpc.Errorf(codes.NotFound, "Emulator spec %q doesn't exist.", specId.Value)
	}
	delete(s.emulators, specId.Value)
	return EMPTY, nil
}

// Lists all specs.
func (s *server) ListEmulatorSpecs(ctx context.Context, _ *pb.Empty) (*emulators.ListEmulatorSpecsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var l []*emulators.EmulatorSpec
	for _, emu := range s.emulators {
		l = append(l, emu.spec)
	}
	return &emulators.ListEmulatorSpecsResponse{Specs: l}, nil
}

func outputLogPrefixer(prefix string, in io.Reader) {
	log.Printf("Broker: Output connected for %q", prefix)
	buffReader := bufio.NewReader(in)
	for {
		line, _, err := buffReader.ReadLine()
		if err != nil {
			log.Printf("Broker: End of stream for %v, (%s).", prefix, err)
			return
		}
		log.Printf("%s: %s", prefix, line)
	}
}

func (s *server) StartEmulator(ctx context.Context, specId *emulators.SpecId) (*pb.Empty, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	id := specId.Value
	log.Printf("Broker: StartEmulator %v.", id)
	emu, exists := s.emulators[id]
	if !exists {
		return nil, grpc.Errorf(codes.FailedPrecondition, "Emulator %q doesn't exist.", id)
	}
	if err := emu.start(); err != nil {
		return nil, err
	}
	log.Printf("Broker: Emulator starting %q", id)
	return EMPTY, nil
}

func (s *server) StopEmulator(ctx context.Context, specId *emulators.SpecId) (*pb.Empty, error) {
	log.Printf("Broker: StopEmulator %v.", specId)
	s.mu.Lock()
	defer s.mu.Unlock()
	id := specId.Value
	emu, exists := s.emulators[id]
	if !exists {
		return nil, grpc.Errorf(codes.FailedPrecondition, "Emulator %q doesn't exist.", id)
	}
	if err := emu.stop(); err != nil {
		return nil, err
	}
	return EMPTY, nil
}

func (s *server) ListEmulators(ctx context.Context, _ *pb.Empty) (*emulators.ListEmulatorsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return nil, nil
}

// Resolves a target according to relevant specs. If no spec apply, the input
// target is returned in the response.
func (s *server) Resolve(ctx context.Context, req *emulators.ResolveRequest) (*emulators.ResolveResponse, error) {
	log.Printf("Broker: Resolve target %v.", req.Target)
	s.mu.Lock()
	defer s.mu.Unlock()
	/*	log.Printf("Resolve %q", req)
		target := []byte(req.Target)
		for _, matcher := range activeFakes {
			for _, regexp := range matcher.regexps {
				matched, err := re.Match(regexp, target)
				if err != nil {
					return nil, err
				}
				if matched {
					res := &emulators.ResolveResponse{
						Target: matcher.target,
					}
					return res, nil
				}
			}
		}*/
	return nil, fmt.Errorf("%s not found", req.Target)
}
