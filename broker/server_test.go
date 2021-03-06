package broker

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	proto "github.com/golang/protobuf/proto"
	context "golang.org/x/net/context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	emulators "google/emulators"
	duration_pb "github.com/golang/protobuf/ptypes/duration"
)

var (
	tmpDir string

	dummyEmulator *emulators.Emulator = &emulators.Emulator{
		EmulatorId: "dummy",
		Rule: &emulators.ResolveRule{
			RuleId:         "dummy_rule",
			TargetPatterns: []string{"pattern1", "pattern2"},
		},
		StartCommand: &emulators.CommandLine{
			Path: "/exepath",
			Args: []string{"arg1", "arg2"},
		},
		StartOnDemand: false,
	}

	realEmulator *emulators.Emulator = &emulators.Emulator{
		EmulatorId: "real",
		Rule: &emulators.ResolveRule{
			RuleId:         "real_rule",
			TargetPatterns: []string{"real_service"},
		},
		StartCommand: &emulators.CommandLine{
			// Path will be initialized by setUp().
			// Note that port substitution is used with the --port argument.
			Args: []string{"--register", "--port={port:real}", "--rule_id=real_rule"},
		},
		StartOnDemand: true,
	}

	brokerConfig *emulators.BrokerConfig = &emulators.BrokerConfig{}
)

// The entrypoint.
func TestMain(m *testing.M) {
	flag.Parse()
	var exitCode int
	err := setUp()
	if err != nil {
		log.Printf("Setup error: %v", err)
		exitCode = 1
	} else {
		exitCode = m.Run()
	}
	tearDown()
	os.Exit(exitCode)
}

func setUp() error {
	tmpDir, err := ioutil.TempDir(os.TempDir(), "server_test")
	if err != nil {
		return fmt.Errorf("Failed to create temp dir: %v", err)
	}
	log.Printf("Created temp dir: %s", tmpDir)
	path, err := buildSampleEmulator(tmpDir)
	if err != nil {
		return fmt.Errorf("Failed to build sample emulator: %v", err)
	}
	log.Printf("Successfully built Sample emulator: %s", path)
	realEmulator.StartCommand.Path = path

	if err != nil {
		return fmt.Errorf("Failed to pick a free port: %v", err)
	}

	return nil
}

func tearDown() {
	err := os.RemoveAll(tmpDir)
	if err == nil {
		log.Printf("Deleted temp dir: %s", tmpDir)
	} else {
		log.Printf("Failed to delete temp dir: %v", err)
	}
}

// Builds the sample emulator so that it can run directly, i.e. NOT via
// "go run". Returns the path to the resulting binary.
func buildSampleEmulator(outputDir string) (string, error) {
	output := filepath.Join(outputDir, "sample_emulator")
	cmd := exec.Command("go", "build", "-o", output, "../cmd/samples/emulator/emulator.go")
	log.Printf("Running: %s", cmd.Args)
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return output, nil
}

// Returns a BrokerConfig message with a default_emulator_start_deadline
// specified in seconds.
func brokerConfigWithDeadline(deadline time.Duration) *emulators.BrokerConfig {
	return &emulators.BrokerConfig{DefaultEmulatorStartDeadline: &duration_pb.Duration{Seconds: int64(deadline.Seconds())}}
}

// Returns the value of realEmulator's --port argument. Should only be called
// after the command has been expanded with port substitutions.
func realEmulatorPort(b *grpcServer) (int, error) {
	emu, exists := b.s.emulators["real"]
	if !exists {
		return 0, fmt.Errorf("Real emulator is not registered with this server.")
	}
	return strconv.Atoi(emu.emulator.StartCommand.Args[1][7:])
}

func TestExpandSpecialTokens(t *testing.T) {
	os.Setenv("TEST_ENV_QUX", "qux")
	defer os.Unsetenv("TEST_ENV_QUX")
	cases := [][]string{
		[]string{"foo", "foo"},
		[]string{"foo:{port:bar}", "foo:42"},
		[]string{"foo:{port:bar}-{port:bar}", "foo:42-42"},
		[]string{"foo:{port:bar}-{port:baz}", "foo:42-43"},
		[]string{"foo:{port:}", "foo:{port:}"},
		[]string{"foo:{env:TEST_ENV_QUX}", "foo:qux"},
		[]string{"foo:{env:TEST_UNDEFINED}", "foo:"},
		[]string{"foo:{env:}", "foo:{env:}"},
		[]string{"{dir:broker}/foo", "brokerDir/foo"},
	}
	portPicker, err := NewPortRangePicker([]*emulators.PortRange{&emulators.PortRange{Begin: 42, End: 44}})
	if err != nil {
		t.Fatal(err)
	}
	expander := newCommandExpander("brokerDir", portPicker)
	for _, c := range cases {
		s := c[0]
		expander.expandSpecialTokens(&s)
		if s != c[1] {
			t.Errorf("Expected %s: %s", c[1], s)
		}
	}
}

func TestComputeResolveResponse(t *testing.T) {
	// Host input, non-secure.
	r, err := computeResolveResponse("foo", &emulators.ResolveRule{ResolvedHost: "bar", RequiresSecureConnection: false})
	if err != nil {
		t.Error(err)
	}
	want := &emulators.ResolveResponse{Target: "bar", RequiresSecureConnection: false}
	if !proto.Equal(r, want) {
		t.Errorf("want = %v, got %v", want, r)
	}

	// Host input, secure.
	r, err = computeResolveResponse("foo", &emulators.ResolveRule{ResolvedHost: "bar", RequiresSecureConnection: true})
	if err != nil {
		t.Error(err)
	}
	want = &emulators.ResolveResponse{Target: "bar", RequiresSecureConnection: true}
	if !proto.Equal(r, want) {
		t.Errorf("want = %v, got %v", want, r)
	}

	// URL input, non-secure
	r, err = computeResolveResponse("http://foo/baz", &emulators.ResolveRule{ResolvedHost: "bar", RequiresSecureConnection: false})
	if err != nil {
		t.Error(err)
	}
	want = &emulators.ResolveResponse{Target: "http://bar/baz", RequiresSecureConnection: false}
	if !proto.Equal(r, want) {
		t.Errorf("want = %v, got %v", want, r)
	}
	r, err = computeResolveResponse("https://foo/baz", &emulators.ResolveRule{ResolvedHost: "bar", RequiresSecureConnection: false})
	if err != nil {
		t.Error(err)
	}
	want = &emulators.ResolveResponse{Target: "http://bar/baz", RequiresSecureConnection: false}
	if !proto.Equal(r, want) {
		t.Errorf("want = %v, got %v", want, r)
	}

	// URL input, secure
	r, err = computeResolveResponse("http://foo/baz", &emulators.ResolveRule{ResolvedHost: "bar", RequiresSecureConnection: true})
	if err != nil {
		t.Error(err)
	}
	want = &emulators.ResolveResponse{Target: "https://bar/baz", RequiresSecureConnection: true}
	if !proto.Equal(r, want) {
		t.Errorf("want = %v, got %v", want, r)
	}
	r, err = computeResolveResponse("https://foo/baz", &emulators.ResolveRule{ResolvedHost: "bar", RequiresSecureConnection: true})
	if err != nil {
		t.Error(err)
	}
	want = &emulators.ResolveResponse{Target: "https://bar/baz", RequiresSecureConnection: true}
	if !proto.Equal(r, want) {
		t.Errorf("want = %v, got %v", want, r)
	}
}

func TestCreateEmulator(t *testing.T) {
	s := New()
	_, err := s.CreateEmulator(nil, dummyEmulator)
	if err != nil {
		t.Error(err)
	}

	got, err := s.GetEmulator(nil, &emulators.EmulatorId{EmulatorId: dummyEmulator.EmulatorId})
	if err != nil {
		t.Error(err)
	}
	if !proto.Equal(got, dummyEmulator) {
		t.Errorf("Failed to find the same emulator; want = %v, got %v", dummyEmulator, got)
	}
}

func TestCreateEmulator_WithInvalidEmulatorId(t *testing.T) {
	s := New()
	dummy := proto.Clone(dummyEmulator).(*emulators.Emulator)
	dummy.EmulatorId = "my/mistake"
	_, err := s.CreateEmulator(nil, dummy)
	if err == nil || grpc.Code(err) != codes.InvalidArgument {
		t.Errorf("Expected InvalidArgument: %v", err)
	}
}

func TestCreateEmulator_WithInvalidTargetPattern(t *testing.T) {
	s := New()
	dummyWithBadRule := proto.Clone(dummyEmulator).(*emulators.Emulator)
	dummyWithBadRule.Rule.TargetPatterns[0] = "["
	_, err := s.CreateEmulator(nil, dummyWithBadRule)
	if err == nil || grpc.Code(err) != codes.InvalidArgument {
		t.Errorf("Expected InvalidArgument: %v", err)
	}
}

func TestCreateEmulator_WhenAlreadyExists(t *testing.T) {
	s := New()
	_, err := s.CreateEmulator(nil, dummyEmulator)
	if err != nil {
		t.Error(err)
	}

	_, err = s.CreateEmulator(nil, dummyEmulator)
	if err == nil {
		t.Errorf("This creation should have failed.")
	}
	if grpc.Code(err) != codes.AlreadyExists {
		t.Errorf("This creation should have failed with AlreadyExists.")
	}
}

func TestGetEmulator_WhenNotFound(t *testing.T) {
	s := New()
	_, err := s.GetEmulator(nil, &emulators.EmulatorId{"whatever"})

	if err == nil {
		t.Errorf("Get of a non existent emulator should have failed.")
	}
	if grpc.Code(err) != codes.NotFound {
		t.Errorf("Get should return NotFound as error")
	}
}

func TestListEmulators(t *testing.T) {
	s := New()
	want1 := &emulators.Emulator{EmulatorId: "foo",
		Rule:         &emulators.ResolveRule{RuleId: "foo_rule"},
		StartCommand: &emulators.CommandLine{Path: "/foo", Args: []string{"arg1", "arg2"}}}
	_, err := s.CreateEmulator(nil, want1)
	if err != nil {
		t.Error(err)
	}

	want2 := &emulators.Emulator{EmulatorId: "bar",
		Rule:         &emulators.ResolveRule{RuleId: "bar_rule"},
		StartCommand: &emulators.CommandLine{Path: "/bar", Args: []string{"arg1", "arg2"}}}
	_, err = s.CreateEmulator(nil, want2)
	if err != nil {
		t.Error(err)
	}

	want := make(map[string]*emulators.Emulator)
	want[want1.EmulatorId] = want1
	want[want2.EmulatorId] = want2

	resp, err := s.ListEmulators(nil, EmptyPb)
	if err != nil {
		t.Error(err)
	}
	got := make(map[string]*emulators.Emulator)
	for _, emu := range resp.Emulators {
		got[emu.EmulatorId] = emu
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStartEmulator(t *testing.T) {
	b, err := startNewBroker(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()

	_, err = b.s.CreateEmulator(nil, realEmulator)
	if err != nil {
		t.Fatal(err)
	}
	emulatorId := emulators.EmulatorId{EmulatorId: realEmulator.EmulatorId}
	_, err = b.s.StartEmulator(nil, &emulatorId)
	if err != nil {
		t.Fatal(err)
	}
	emu, err := b.s.GetEmulator(nil, &emulatorId)
	if err != nil {
		t.Fatal(err)
	}
	if emu.State != emulators.Emulator_ONLINE {
		t.Errorf("Expected ONLINE: %s", emu.State)
	}
}

func TestStartEmulator_WhenNotFound(t *testing.T) {
	s := New()
	_, err := s.StartEmulator(nil, &emulators.EmulatorId{EmulatorId: "foo"})
	if err == nil || grpc.Code(err) != codes.NotFound {
		t.Errorf("Expected NotFound: %v", err)
	}
}

func TestStartEmulator_WhenAlreadyStarting(t *testing.T) {
	b, err := startNewBroker(brokerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()

	realWithWait := proto.Clone(realEmulator).(*emulators.Emulator)
	realWithWait.StartCommand.Args = append(realWithWait.StartCommand.Args, "--wait")
	_, err = b.s.CreateEmulator(nil, realWithWait)
	if err != nil {
		t.Error(err)
	}

	// Start the emulator in two separate threads. The operation should not
	// complete initially
	done := make(chan bool, 1)
	start := func(i int) {
		_, err = b.s.StartEmulator(nil, &emulators.EmulatorId{EmulatorId: realWithWait.EmulatorId})
		if err != nil {
			t.Errorf("T%d saw error: %v", i, err)
		}
		done <- true
	}
	go start(0)
	go start(1)

	select {
	case <-done:
		t.Fatal("Emulator started unexpectedly - should have waited for explicit indicator!")
	case <-time.After(time.Second):
		break
	}

	// Signal the start to complete. Both threads should finish up.
	port, err := realEmulatorPort(b)
	if err != nil {
		t.Fatal(err)
	}
	_, err = http.Get(fmt.Sprintf("http://localhost:%d/setStatusOk", port))
	if err != nil {
		log.Fatal("Failed to indicate emulator has started: %v", err)
	}
	for count := 0; count < 2; {
		select {
		case <-done:
			count++
			break
		case <-time.After(time.Second):
			t.Fatal("StartEmulator() did not return as expected!")
		}
	}
}

func TestStartEmulator_WhenAlreadyOnline(t *testing.T) {
	b, err := startNewBroker(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()

	_, err = b.s.CreateEmulator(nil, realEmulator)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.s.StartEmulator(nil, &emulators.EmulatorId{EmulatorId: realEmulator.EmulatorId})
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.s.StartEmulator(nil, &emulators.EmulatorId{EmulatorId: realEmulator.EmulatorId})
	if err == nil || grpc.Code(err) != codes.AlreadyExists {
		t.Errorf("Expected AlreadyExists: %v", err)
	}
}

func TestStartEmulator_WhenDefaultStartDeadlineElapses(t *testing.T) {
	b, err := startNewBroker(brokerConfigWithDeadline(1 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()

	_, err = b.s.CreateEmulator(nil, dummyEmulator)
	if err != nil {
		t.Error(err)
	}
	_, err = b.s.StartEmulator(nil, &emulators.EmulatorId{EmulatorId: dummyEmulator.EmulatorId})
	if err == nil || grpc.Code(err) != codes.DeadlineExceeded {
		t.Errorf("Expected DeadlineExceeded: %v", err)
	}
}

func TestStartEmulator_WhenContextDeadlineElapses(t *testing.T) {
	b, err := startNewBroker(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()

	_, err = b.s.CreateEmulator(nil, dummyEmulator)
	if err != nil {
		t.Error(err)
	}
	ctx, _ := context.WithTimeout(context.Background(), 1*time.Second)
	_, err = b.s.StartEmulator(ctx, &emulators.EmulatorId{EmulatorId: dummyEmulator.EmulatorId})
	if err == nil || grpc.Code(err) != codes.DeadlineExceeded {
		t.Errorf("Expected DeadlineExceeded: %v", err)
	}
}

func TestReportEmulatorOnline(t *testing.T) {
	s := New()
	_, err := s.CreateEmulator(nil, dummyEmulator)
	if err != nil {
		t.Error(err)
	}

	s.emulators[dummyEmulator.EmulatorId].markStartingForTest()

	req := emulators.ReportEmulatorOnlineRequest{
		EmulatorId:     dummyEmulator.EmulatorId,
		TargetPatterns: []string{"newPattern"},
		ResolvedHost:   "t"}
	_, err = s.ReportEmulatorOnline(nil, &req)
	if err != nil {
		t.Errorf("Reporting emulator online should not have failed. %v", err)
	}

	rule, err := s.GetResolveRule(nil, &emulators.ResolveRuleId{RuleId: dummyEmulator.Rule.RuleId})
	if err != nil {
		t.Error(err)
	}
	got := rule.ResolvedHost
	want := req.ResolvedHost
	if got != want {
		t.Error("Want %q but got %q", want, got)
	}
	if len(rule.TargetPatterns) != len(dummyEmulator.Rule.TargetPatterns)+len(req.TargetPatterns) {
		t.Error("Target patterns were not merged correctly: %v", rule.TargetPatterns)
	}
}

func TestReportEmulatorOnline_WhenNotFound(t *testing.T) {
	s := New()
	req := emulators.ReportEmulatorOnlineRequest{
		EmulatorId:   dummyEmulator.EmulatorId,
		ResolvedHost: "t"}
	_, err := s.ReportEmulatorOnline(nil, &req)
	if err == nil || grpc.Code(err) != codes.NotFound {
		t.Errorf("Expected NotFound: %v", err)
	}
}

func TestReportEmulatorOnline_WhenOffline(t *testing.T) {
	s := New()
	_, err := s.CreateEmulator(nil, dummyEmulator)
	if err != nil {
		t.Error(err)
	}

	req := emulators.ReportEmulatorOnlineRequest{
		EmulatorId:   dummyEmulator.EmulatorId,
		ResolvedHost: "t"}
	_, err = s.ReportEmulatorOnline(nil, &req)
	if err == nil || grpc.Code(err) != codes.FailedPrecondition {
		t.Errorf("Expected FailedPrecondition: %v", err)
	}
}

func TestReportEmulatorOnline_WhenStarted(t *testing.T) {
	s := New()
	_, err := s.CreateEmulator(nil, dummyEmulator)
	if err != nil {
		t.Error(err)
	}

	emu, _ := s.emulators[dummyEmulator.EmulatorId]
	emu.markStartingForTest()
	emu.markOnline()

	req := emulators.ReportEmulatorOnlineRequest{
		EmulatorId:   dummyEmulator.EmulatorId,
		ResolvedHost: "t"}
	_, err = s.ReportEmulatorOnline(nil, &req)
	if err == nil || grpc.Code(err) != codes.FailedPrecondition {
		t.Errorf("Expected FailedPrecondition: %v", err)
	}
}

func TestStopEmulator(t *testing.T) {
	b, err := startNewBroker(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()

	_, err = b.s.CreateEmulator(nil, realEmulator)
	if err != nil {
		t.Fatal(err)
	}
	emulatorId := emulators.EmulatorId{EmulatorId: realEmulator.EmulatorId}
	_, err = b.s.StartEmulator(nil, &emulatorId)
	if err != nil {
		t.Fatal(err)
	}
	emu, err := b.s.GetEmulator(nil, &emulatorId)
	if err != nil {
		t.Fatal(err)
	}
	if emu.Rule.ResolvedHost == "" {
		t.Fatal("Expected non-empty resolved host")
	}
	_, err = b.s.StopEmulator(nil, &emulatorId)
	if err != nil {
		t.Fatal(err)
	}
	emu, err = b.s.GetEmulator(nil, &emulatorId)
	if err != nil {
		t.Fatal(err)
	}
	if emu.State != emulators.Emulator_OFFLINE {
		t.Errorf("Expected OFFLINE: %s", emu.State)
	}
	emu, err = b.s.GetEmulator(nil, &emulatorId)
	if err != nil {
		t.Fatal(err)
	}
	if emu.Rule.ResolvedHost != "" {
		t.Fatal("Expected empty resolved host")
	}
	// Restart the emulator.
	_, err = b.s.StartEmulator(nil, &emulatorId)
	if err != nil {
		t.Fatal(err)
	}
	emu, err = b.s.GetEmulator(nil, &emulatorId)
	if err != nil {
		t.Fatal(err)
	}
	if emu.Rule.ResolvedHost == "" {
		t.Fatal("Expected non-empty resolved host")
	}
}

func TestStopEmulator_WhenNotFound(t *testing.T) {
	s := New()
	_, err := s.StopEmulator(nil, &emulators.EmulatorId{EmulatorId: dummyEmulator.EmulatorId})
	if err == nil || grpc.Code(err) != codes.NotFound {
		t.Errorf("Expected NotFound: %v", err)
	}
}

func TestStopEmulator_WhenOffline(t *testing.T) {
	s := New()
	_, err := s.CreateEmulator(nil, dummyEmulator)
	if err != nil {
		t.Error(err)
	}
	_, err = s.StopEmulator(nil, &emulators.EmulatorId{EmulatorId: dummyEmulator.EmulatorId})
	if err != nil {
		t.Error(err)
	}
}

func TestCreateResolveRule(t *testing.T) {
	s := New()
	rule := dummyEmulator.Rule
	_, err := s.CreateResolveRule(nil, rule)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetResolveRule(nil, &emulators.ResolveRuleId{RuleId: rule.RuleId})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, rule) {
		t.Errorf("Failed to find the same rule; want = %v, got %v", rule, got)
	}
}

func TestCreateResolveRule_WithInvalidRuleId(t *testing.T) {
	s := New()
	badRule := emulators.ResolveRule{RuleId: "my/mistake", TargetPatterns: []string{"pattern1"}}
	_, err := s.CreateResolveRule(nil, &badRule)
	if err == nil || grpc.Code(err) != codes.InvalidArgument {
		t.Fatalf("Expected InvalidArgument: %v", err)
	}
}

func TestCreateResolveRule_WithInvalidTargetPattern(t *testing.T) {
	s := New()
	badRule := emulators.ResolveRule{RuleId: "bad", TargetPatterns: []string{"["}}
	_, err := s.CreateResolveRule(nil, &badRule)
	if err == nil || grpc.Code(err) != codes.InvalidArgument {
		t.Fatalf("Expected InvalidArgument: %v", err)
	}
}

func TestCreateResolveRule_WhenIdenticalRuleAlreadyExists(t *testing.T) {
	s := New()
	rule := dummyEmulator.Rule
	_, err := s.CreateResolveRule(nil, rule)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateResolveRule(nil, rule)
	if err != nil {
		t.Errorf("Expected OK: %v", err)
	}
}

func TestCreateResolveRule_WhenDifferentRuleAlreadyExists(t *testing.T) {
	s := New()
	rule := *dummyEmulator.Rule
	_, err := s.CreateResolveRule(nil, &rule)
	if err != nil {
		t.Fatal(err)
	}
	rule.ResolvedHost = rule.ResolvedHost + "-2"
	_, err = s.CreateResolveRule(nil, &rule)
	if err == nil || grpc.Code(err) != codes.AlreadyExists {
		t.Errorf("Expected AlreadyExists: %v", err)
	}
}

func TestGetResolveRule_WhenNotFound(t *testing.T) {
	s := New()
	_, err := s.GetResolveRule(nil, &emulators.ResolveRuleId{RuleId: dummyEmulator.Rule.RuleId})
	if err == nil || grpc.Code(err) != codes.NotFound {
		t.Errorf("Expected NotFound: %v", err)
	}
}

func TestUpdateResolveRule(t *testing.T) {
	s := New()
	rule := *dummyEmulator.Rule
	_, err := s.CreateResolveRule(nil, &rule)
	if err != nil {
		t.Fatal(err)
	}
	// Specify just a single pattern. This should get merged with the existing
	// ones.
	rule.TargetPatterns = []string{"newPattern"}
	updateResp, err := s.UpdateResolveRule(nil, &rule)
	if err != nil {
		t.Fatalf("Failed to update rule: %v", err)
	}
	getResp, err := s.GetResolveRule(nil, &emulators.ResolveRuleId{RuleId: rule.RuleId})
	if err != nil {
		t.Fatalf("Failed to get rule: %v", err)
	}
	if !proto.Equal(updateResp, getResp) {
		t.Fatalf("Expected %v: %v", getResp, updateResp)
	}
	patterns := merge(dummyEmulator.Rule.TargetPatterns, rule.TargetPatterns)
	if !unorderedEqual(getResp.TargetPatterns, patterns) {
		t.Fatalf("Expected %v: %v", patterns, getResp.TargetPatterns)
	}
}

func TestUpdateResolveRule_WhenNotFound(t *testing.T) {
	s := New()
	_, err := s.UpdateResolveRule(nil, dummyEmulator.Rule)
	if err == nil || grpc.Code(err) != codes.NotFound {
		t.Errorf("Expected NotFound: %v", err)
	}
}

func TestListResolveRules(t *testing.T) {
	s := New()
	resp, err := s.ListResolveRules(nil, EmptyPb)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Rules) != 0 {
		t.Fatalf("Expected no rules: %s", resp.Rules)
	}
	ruleIds := []string{"foo", "bar"}
	for _, id := range ruleIds {
		_, err = s.CreateResolveRule(nil, &emulators.ResolveRule{RuleId: id})
		if err != nil {
			t.Fatalf("Failed to create rule %q: %v", id, err)
		}
	}
	resp, err = s.ListResolveRules(nil, EmptyPb)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Rules) != 2 {
		t.Fatalf("Expected 2 rules: %s", resp.Rules)
	}
	actualIds := []string{}
	for _, rule := range resp.Rules {
		actualIds = append(actualIds, rule.RuleId)
	}
	sort.Strings(ruleIds)
	sort.Strings(actualIds)
	if !reflect.DeepEqual(ruleIds, actualIds) {
		t.Errorf("Expected %s: %s", ruleIds, actualIds)
	}
}

func TestResolve_NoMatches(t *testing.T) {
	s := New()
	req := emulators.ResolveRequest{Target: "foo"}
	resp, err := s.Resolve(nil, &req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Target != req.Target {
		t.Errorf("Expected %q: %s", req.Target, resp.Target)
	}
}

func TestResolve_EmulatorOffline(t *testing.T) {
	b, err := startNewBroker(brokerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()

	_, err = b.s.CreateEmulator(nil, realEmulator)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := b.s.Resolve(nil, &emulators.ResolveRequest{Target: realEmulator.Rule.TargetPatterns[0]})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	port, err := realEmulatorPort(b)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("localhost:%d", port)
	if resp.Target != want {
		t.Errorf("Wrong resolved target: %s (want: %s)", resp.Target, want)
	}
}

func TestResolve_WhenDefaultStartDeadlineElapses(t *testing.T) {
	b, err := startNewBroker(brokerConfigWithDeadline(1 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()

	realWithWait := proto.Clone(realEmulator).(*emulators.Emulator)
	realWithWait.StartCommand.Args = append(realWithWait.StartCommand.Args, "--wait")
	_, err = b.s.CreateEmulator(nil, realWithWait)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.s.Resolve(nil, &emulators.ResolveRequest{Target: realWithWait.Rule.TargetPatterns[0]})
	if err == nil || grpc.Code(err) != codes.Unavailable {
		t.Errorf("Expected Unavailable: %v", err)
	}
}

// The resolve operation should wait for the start operation to complete.
func TestResolve_EmulatorStarting(t *testing.T) {
	b, err := startNewBroker(brokerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()

	realWithWait := proto.Clone(realEmulator).(*emulators.Emulator)
	realWithWait.StartCommand.Args = append(realWithWait.StartCommand.Args, "--wait")
	_, err = b.s.CreateEmulator(nil, realWithWait)
	if err != nil {
		t.Fatal(err)
	}

	// Start the emulator, which waits to be signaled to indicate it is online.
	// Then start the resolve.
	emulatorId := emulators.EmulatorId{EmulatorId: realWithWait.EmulatorId}
	startDone := make(chan bool, 1)
	go func() {
		_, startErr := b.s.StartEmulator(nil, &emulatorId)
		if startErr != nil {
			t.Fatalf("Start failed: %v", startErr)
		}
		startDone <- true
	}()

	resolveDone := make(chan *emulators.ResolveResponse, 1)
	go func() {
		waitErr := b.s.waitForStarting(realWithWait.EmulatorId, time.Now().Add(5*time.Second))
		if waitErr != nil {
			t.Fatal(waitErr)
		}
		resp, resolveErr := b.s.Resolve(nil, &emulators.ResolveRequest{Target: realWithWait.Rule.TargetPatterns[0]})
		if resolveErr != nil {
			t.Fatalf("Resolve failed: %v", resolveErr)
		}
		resolveDone <- resp
	}()

	// Neither the start nor the resolve operation complete initially.
	select {
	case <-startDone:
		t.Fatal("Start completed unexpectedly!")
	case <-resolveDone:
		t.Fatal("Resolve completed unexpectedly!")
	case <-time.After(1 * time.Second):
		break
	}

	port, err := realEmulatorPort(b)
	if err != nil {
		t.Fatal(err)
	}
	http.Get(fmt.Sprintf("http://localhost:%d/setStatusOk", port))
	if err != nil {
		log.Fatal("Failed to indicate emulator has started: %v", err)
	}

	// Now the operations should complete swiftly.
	<-startDone
	resp := <-resolveDone
	want := fmt.Sprintf("localhost:%d", port)
	if resp.Target != want {
		t.Errorf("Expected %q: %s", want, resp.Target)
	}
}

func TestResolve_EmulatorOnline(t *testing.T) {
	b, err := startNewBroker(brokerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()

	_, err = b.s.CreateEmulator(nil, realEmulator)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.s.Resolve(nil, &emulators.ResolveRequest{Target: realEmulator.Rule.TargetPatterns[0]})
	if err != nil {
		t.Fatal(err)
	}
	emulatorId := emulators.EmulatorId{EmulatorId: realEmulator.EmulatorId}
	emu, err := b.s.GetEmulator(nil, &emulatorId)
	if err != nil {
		t.Fatal(err)
	}
	if emu.State != emulators.Emulator_ONLINE {
		t.Fatalf("Expected emulator to be ONLINE: %s", emu.State)
	}
	// Now resolve again.
	resp, err := b.s.Resolve(nil, &emulators.ResolveRequest{Target: realEmulator.Rule.TargetPatterns[0]})
	if err != nil {
		t.Fatal(err)
	}
	port, err := realEmulatorPort(b)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("localhost:%d", port)
	if resp.Target != want {
		t.Errorf("Expected %q: %s", want, resp.Target)
	}
}

func TestResolve_EmulatorDoesNotStartOnDemand(t *testing.T) {
	s := New()
	_, err := s.CreateEmulator(nil, dummyEmulator)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Resolve(nil, &emulators.ResolveRequest{Target: dummyEmulator.Rule.TargetPatterns[0]})
	if err == nil || grpc.Code(err) != codes.Unavailable {
		t.Errorf("Expected Unavailable: %v", err)
	}
}
