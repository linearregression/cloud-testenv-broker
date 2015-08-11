package broker

import (
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	emulators "google/emulators"
	"io/ioutil"
	"log"
	"net"
	"os"
	"reflect"
	"testing"
	"time"
)

var dummySpec *emulators.EmulatorSpec = &emulators.EmulatorSpec{
	Id:            "foo",
	TargetPattern: []string{"foo*./", "bar*./"},
	CommandLine: &emulators.CommandLine{
		Path: "/exepath",
		Args: []string{"arg1", "arg2"},
	},
}

func TestCreateSpec(t *testing.T) {

	s := New()
	want := dummySpec
	req := &emulators.CreateEmulatorSpecRequest{
		SpecId: "foo",
		Spec:   want}
	spec, err := s.CreateEmulatorSpec(nil, req)

	if err != nil {
		t.Error(err)
	}

	got, err := s.GetEmulatorSpec(nil, &emulators.SpecId{spec.Id})

	if err != nil {
		t.Error(err)
	}

	if got != want {
		t.Errorf("Failed to find back the same spec want = %v, got %v", want, got)
	}
}

func TestDoubleCreateSpec(t *testing.T) {

	s := New()
	want := dummySpec
	req := &emulators.CreateEmulatorSpecRequest{
		SpecId: "foo",
		Spec:   want}
	_, err := s.CreateEmulatorSpec(nil, req)

	if err != nil {
		t.Error(err)
	}

	spec, err := s.CreateEmulatorSpec(nil, req)

	if err == nil {
		t.Errorf("This creation should have failed.")
	}

	if grpc.Code(err) != codes.AlreadyExists {
		t.Errorf("This creation should have failed with AlreadyExists.")
	}

	if spec != nil {
		t.Errorf("It should not have returned a spec %q.", spec)
	}
}

func TestMissingSpec(t *testing.T) {
	s := New()
	_, err := s.GetEmulatorSpec(nil, &emulators.SpecId{"whatever"})

	if err == nil {
		t.Errorf("Get of a non existent spec should have failed.")
	}
	if grpc.Code(err) != codes.NotFound {
		t.Errorf("Get should return NotFound as error")
	}

}

func TestUpdateMissingSpec(t *testing.T) {
	s := New()
	_, err := s.UpdateEmulatorSpec(nil, dummySpec)

	if err == nil {
		t.Errorf("Update of a non existent spec should have failed.")
	}
	if grpc.Code(err) != codes.NotFound {
		t.Errorf("Get should return NotFound as error")
	}

}

func TestDeleteMissingSpec(t *testing.T) {
	s := New()
	_, err := s.DeleteEmulatorSpec(nil, &emulators.SpecId{"whatever"})

	if err == nil {
		t.Errorf("Delete of a non existent spec should have failed.")
	}
	if grpc.Code(err) != codes.NotFound {
		t.Errorf("Get should return NotFound as error")
	}

}

func TestDeleteSpec(t *testing.T) {
	s := New()
	req := &emulators.CreateEmulatorSpecRequest{
		SpecId: "foo",
		Spec:   dummySpec}
	spec, err := s.CreateEmulatorSpec(nil, req)

	if err != nil {
		t.Error(err)
	}
	_, err = s.DeleteEmulatorSpec(nil, &emulators.SpecId{"foo"})

	if err != nil {
		t.Error(err)
	}

	_, err = s.GetEmulatorSpec(nil, &emulators.SpecId{spec.Id})
	if err == nil {
		t.Errorf("Get of a spec  after deletion should have failed.")
	}
	if grpc.Code(err) != codes.NotFound {
		t.Errorf("Get should return NotFound as error")
	}

}

func TestUpdateSpec(t *testing.T) {
	s := New()
	req := &emulators.CreateEmulatorSpecRequest{
		SpecId: "foo",
		Spec:   dummySpec}
	_, err := s.CreateEmulatorSpec(nil, req)

	if err != nil {
		t.Error(err)
	}
	modifiedSpec := *dummySpec
	want := "somethingElse"

	modifiedSpec.ResolvedTarget = want
	_, err = s.UpdateEmulatorSpec(nil, &modifiedSpec)

	if err != nil {
		t.Errorf("Update of an existent spec should not have failed. %v", err)
	}

	newSpec, err := s.GetEmulatorSpec(nil, &emulators.SpecId{dummySpec.Id})

	if err != nil {
		t.Error(err)
	}
	got := newSpec.ResolvedTarget
	if got != want {
		t.Error("Want %q but got %q", want, got)
	}
}

func TestListSpec(t *testing.T) {

	s := New()
	want1 := &emulators.EmulatorSpec{
		Id:            "foo",
		TargetPattern: []string{"foo*./", "bar*./"},
		CommandLine: &emulators.CommandLine{
			Path: "/exepath",
			Args: []string{"arg1", "arg2"},
		},
	}

	req := &emulators.CreateEmulatorSpecRequest{
		SpecId: "foo",
		Spec:   want1}
	_, err := s.CreateEmulatorSpec(nil, req)
	if err != nil {
		t.Error(err)
	}

	want2 := &emulators.EmulatorSpec{
		Id:            "bar",
		TargetPattern: []string{"baz*./", "taz*./"},
		CommandLine: &emulators.CommandLine{
			Path: "/exepathbar",
			Args: []string{"arg1", "arg2"},
		},
	}

	req = &emulators.CreateEmulatorSpecRequest{
		SpecId: "bar",
		Spec:   want2}
	_, err = s.CreateEmulatorSpec(nil, req)

	if err != nil {
		t.Error(err)
	}

	resp, err := s.ListEmulatorSpecs(nil, EMPTY)
	if err != nil {
		t.Error(err)
	}
	want := make(map[string]*emulators.EmulatorSpec)
	want[want1.Id] = want1
	want[want2.Id] = want2

	got := make(map[string]*emulators.EmulatorSpec)
	for _, spec := range resp.Specs {
		got[spec.Id] = spec
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStartEmulator(t *testing.T) {
	s := New()
	dir, err := ioutil.TempDir("", "broker-test")
	if err != nil {
		t.Error(err)
	}
	filename := dir + "testfile"

	emu := &emulators.EmulatorSpec{
		Id:            "toucher",
		TargetPattern: []string{""},
		CommandLine: &emulators.CommandLine{
			Path: "/usr/bin/touch",
			Args: []string{filename},
		},
	}

	req := &emulators.CreateEmulatorSpecRequest{
		SpecId: "toucher",
		Spec:   emu}

	_, err = s.CreateEmulatorSpec(nil, req)
	if err != nil {
		t.Error(err)
	}
	_, err = s.StartEmulator(nil, &emulators.SpecId{"toucher"})
	if err != nil {
		t.Error(err)
	}
	time.Sleep(time.Second) // FIXME: this might be flaky

	if _, err := os.Stat(filename); os.IsNotExist(err) {
		t.Errorf("Emulator did not start: no file %q has been created.", filename)
	}
}

func TestEndToEndRegisterEmulator(t *testing.T) {
	s := New()
	id := "end2end"
	lis, err := net.Listen("tcp", ":10000")
	if err != nil {
		log.Fatalf("failed to listen: %v.", err)
	}

	grpcServer := grpc.NewServer()
	emulators.RegisterBrokerServer(grpcServer, s)
	go grpcServer.Serve(lis)
	time.Sleep(5 * time.Second) // FIXME: this might be flaky

	spec := &emulators.EmulatorSpec{
		Id:            id,
		TargetPattern: []string{""},
		CommandLine: &emulators.CommandLine{
			//Path: "echo",
			// Args: []string{"toto"},
			Path: "go",
			Args: []string{"run", "../samples/emulator/main.go", "--register", "--port=12345", "--spec_id=" + id},
		},
	}
	req := &emulators.CreateEmulatorSpecRequest{
		SpecId: id,
		Spec:   spec}

	_, err = s.CreateEmulatorSpec(nil, req)
	if err != nil {
		t.Error(err)
	}
	_, err = s.StartEmulator(nil, &emulators.SpecId{id})
	if err != nil {
		t.Error(err)
	}
	time.Sleep(10 * time.Second) // FIXME: this might be flaky

	updatedSpec, err := s.GetEmulatorSpec(nil, &emulators.SpecId{spec.Id})

	if err != nil {
		t.Error(err)
	}
	got := updatedSpec.ResolvedTarget
	want := "localhost:12345"

	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
