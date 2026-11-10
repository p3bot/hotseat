// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

const (
	fromOwner        = "owner"
	fromRun          = "run"
	kindInstructions = "instructions"
	kindTurn         = "turn"
	kindDone         = "done"
	kindError        = "error"
	kindSay          = "say"
	kindFinding      = "finding"
	kindAsk          = "ask"
	txInstructions   = "instructions"

	kindPropose  = "propose"
	kindDismiss  = "dismiss"
	kindAgree    = "agree"
	kindObject   = "object"
	kindStand    = "stand"
	kindPass     = "pass"
	kindExtend   = "extend"
	kindDeadlock = "deadlock"
	kindFixed    = "fixed"
	kindDiscuss  = "discuss"
	kindSettled  = "settled"
	kindHalted   = "halted"

	roleMember = "hotseat/member"
	contextBus = "hotseat/bus"

	statusLaunched   = "launched"
	statusRegistered = "registered"
	statusRunning    = "running"
	statusExited     = "exited"
	statusTimedOut   = "timed-out"

	classProcessExit    = "process exit"
	classProcessLimit   = "process limit"
	classMissingSession = "missing session id"
	classBadTurn        = "bad turn"
	classTurnCap        = "turn cap"
	classNoBackup       = "no backup"
	classBelowMinimum   = "below minimum"
	classLaunchCap      = "launch cap"

	classBadFinding  = "bad finding"
	classNoStance    = "no stance"
	classExtraStance = "extra stance"
	classBadStance   = "bad stance"
	classBadPass     = "bad pass"
	classBadExtend   = "bad extend"
	classBadDeadlock = "bad deadlock"
	classNoDecision  = "no decision"
	classNoFix       = "no fix"
	classNoReport    = "no report"
	classUnsettled   = "unsettled"
)

// stanceKinds are the kinds a discussion launch publishes one of. Prompts read them with findings on an unnamed read.
var stanceKinds = []string{kindPropose, kindDismiss, kindAgree, kindObject, kindStand}

// Execute runs one conversation.
// stdout is key: value status. A usage failure writes nothing there.
// The token value is not written to stdout, stderr, or a prompt.
func Execute(ctx context.Context, cfg Config, stdout, stderr io.Writer) error {
	return executeWithGrace(ctx, cfg, DoneGrace, stdout, stderr)
}

// executeWithGrace is Execute with the done sender's grace period. Only tests pass a period other than DoneGrace.
func executeWithGrace(ctx context.Context, cfg Config, grace time.Duration, stdout, stderr io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	e, err := newEngine(ctx, cfg, grace, stdout, stderr)
	if err != nil {
		return err
	}
	err = e.execute()
	if werr := e.out.Err(); werr != nil && err == nil {
		err = werr
	}
	return err
}

type engine struct {
	root   context.Context
	child  context.Context
	cancel context.CancelFunc

	cfg       Config
	addr      string
	token     string
	tokenFile string
	perm      string
	limit     time.Duration
	grace     time.Duration
	maxTurns  int
	maxLaunch int
	maxSays   int
	dir       string
	startBin  string
	tkBin     string
	stderr    io.Writer
	out       *statusWriter

	roster []agent
	seat   string
	// minimum is the task's min_members. It is set before any launch, and zero adds no check.
	minimum int

	name       string
	binding    client.Conversation
	continuing bool
	started    bool

	mu       sync.Mutex
	status   map[string]string
	sessions map[string]string
	slots    map[string]*slot
	acted    int
	haltErr  error
	outcome  bool
	closing  bool
	doneFrom string
	// doneSeq is the stopping done. Only messages stored before it count toward the stop result.
	doneSeq     int64
	handoffFrom string
	launches    int
	// inFlight counts member processes this run started or watches and has not yet recorded.
	inFlight  int
	windClass string
	// idle is closed once windClass is set and inFlight is zero.
	idle       chan struct{}
	idleClosed bool

	// judged holds the seqs a live launch window already ruled on, so the transcript loop does not report them again.
	judged map[int64]bool
	// holding keeps a queued handoff from launching while a set's discussions run.
	holding bool
	// leftovers is closed once every process left from an earlier run has exited and been recorded.
	leftovers chan struct{}
	// historyHead is the newest seq stored when a continuation started. Every message at or before it is history.
	historyHead int64
	// leftoverEnds maps each leftover to the newest seq stored once its exit was recorded, or MaxInt64 while it runs.
	leftoverEnds map[string]int64
	// seen is the newest seq this process has read.
	seen int64
	// backlog is the newest seq judgeBacklog has checked.
	backlog int64
	// protocol holds the ledger kinds read so far, in seq order. A stored message never changes, so a build reads only later ones.
	protocol []client.Message
}

type slot struct {
	mu sync.Mutex
}

func newEngine(ctx context.Context, cfg Config, grace time.Duration, stdout, stderr io.Writer) (*engine, error) {
	if cfg.Dir == "" {
		return nil, errors.New("working directory is required")
	}
	if cfg.MaxTurnsSet && cfg.MaxTurns < 1 {
		return nil, errors.New("max turns must be a positive integer")
	}
	if cfg.MaxLaunchesSet && cfg.MaxLaunches < 1 {
		return nil, errors.New("max launches must be a positive integer")
	}
	if cfg.MaxSaysSet && cfg.MaxSays < 1 {
		return nil, errors.New("max says must be a positive integer")
	}
	if cfg.TimeoutSet && cfg.Timeout <= 0 {
		return nil, errors.New("timeout must be positive")
	}
	addr := cfg.Address
	if addr == "" {
		addr = bus.DefaultListen
	}
	var token string
	if cfg.TokenFileSet || cfg.TokenFile != "" {
		tok, err := bus.ReadTokenFile(cfg.TokenFile)
		if err != nil {
			return nil, err
		}
		token = tok
	}
	startBin := cfg.StartBin
	if startBin == "" {
		startBin = "start"
	}
	tkBin := cfg.TkBin
	if tkBin == "" {
		tkBin = "tk"
	}
	child, cancel := context.WithCancel(ctx)
	e := &engine{
		root:         ctx,
		child:        child,
		cancel:       cancel,
		cfg:          cfg,
		addr:         addr,
		token:        token,
		tokenFile:    cfg.TokenFile,
		perm:         cfg.permission(),
		limit:        cfg.timeout(),
		grace:        grace,
		maxTurns:     cfg.maxTurns(),
		maxLaunch:    cfg.maxLaunches(),
		maxSays:      cfg.maxSays(),
		startBin:     startBin,
		tkBin:        tkBin,
		stderr:       &lockedWriter{w: stderr},
		out:          &statusWriter{w: stdout},
		status:       make(map[string]string),
		sessions:     make(map[string]string),
		slots:        make(map[string]*slot),
		idle:         make(chan struct{}),
		judged:       make(map[int64]bool),
		leftovers:    make(chan struct{}),
		leftoverEnds: make(map[string]int64),
	}
	return e, nil
}

func (e *engine) execute() error {
	defer e.cancel()
	var err error
	if e.cfg.AgentsSet {
		e.roster, err = parseAgents(e.cfg.Agents)
		if err != nil {
			return err
		}
	}
	if e.cfg.SeatSet {
		if e.cfg.Seat != strings.ToLower(e.cfg.Seat) || !validName(e.cfg.Seat) {
			return fmt.Errorf("seat %s must be a lowercase hotseat name", e.cfg.Seat)
		}
	}
	if e.cfg.ConversationSet {
		return e.continueConversation()
	}
	if !e.cfg.TaskSet || strings.TrimSpace(e.cfg.Task) == "" {
		return errors.New("a task is required")
	}
	return e.createConversation(true)
}

func (e *engine) continueConversation() error {
	if err := checkName(e.cfg.Conversation); err != nil {
		return err
	}
	conv, ok, err := e.find(e.cfg.Conversation)
	if err != nil {
		return err
	}
	if !ok {
		if !e.cfg.TaskSet || strings.TrimSpace(e.cfg.Task) == "" {
			return errors.New("a task is required when the conversation does not exist")
		}
		return e.createConversation(false)
	}
	if err := e.agree(conv); err != nil {
		return err
	}
	e.binding = conv
	e.name = conv.Name
	e.seat = conv.Seat
	e.dir = conv.Directory
	e.continuing = true
	e.started = true
	if err := e.ensureRoster(conv.Roster); err != nil {
		return err
	}
	if err := e.ensureInstructions(conv); err != nil {
		return e.fail(err)
	}
	if err := e.loadMembers(); err != nil {
		return e.fail(err)
	}
	return e.fail(e.drive())
}

func (e *engine) createConversation(suffix bool) error {
	if len(e.roster) == 0 {
		return errors.New("a roster is required")
	}
	seat := e.cfg.Seat
	if !e.cfg.SeatSet {
		seat = defaultSeat(e.roster)
	} else if !nameIn(e.roster, seat) {
		return fmt.Errorf("seat %s is not in the roster", seat)
	}
	e.seat = seat
	if err := e.checkPermissions(); err != nil {
		return err
	}
	var id string
	var err error
	if e.cfg.ConversationSet && e.continuing {
		id = e.binding.Task
	}
	if id == "" {
		id, err = e.taskID(e.cfg.Task)
		if err != nil {
			return err
		}
	}
	if err := e.requireMinimum(id); err != nil {
		return err
	}
	ticket := ""
	if e.cfg.TicketSet {
		ticket = e.cfg.Ticket
	}
	dir, err := e.launchDirectory(ticket)
	if err != nil {
		return err
	}
	e.dir = dir
	prompts := e.promptList()
	text, err := e.moduleText(id)
	if err != nil {
		return err
	}
	custom := ""
	if e.cfg.InstructionsSet {
		custom = e.cfg.Instructions
	}
	body := instructionBody(text, seat, ticket, prompts, custom)
	if len(body) > bus.DefaultMaxBody {
		return fmt.Errorf("instruction body exceeds %d bytes", bus.DefaultMaxBody)
	}
	var name string
	if suffix {
		slug, err := taskSlug(id)
		if err != nil {
			return err
		}
		name, err = e.mint(bareConversation(slug, ticket), id)
		if err != nil {
			return err
		}
	} else {
		name = e.cfg.Conversation
		if err := checkName(name); err != nil {
			return err
		}
		res, err := e.create(name, id, ticket, seat, dir, prompts, custom)
		if err != nil {
			return err
		}
		if res.AlreadyExisted != nil && *res.AlreadyExisted {
			return fmt.Errorf("conversation %s already exists", name)
		}
		e.started = true
		e.name = name
		if err := e.publishInstructions(body, rosterNames(e.roster)); err != nil {
			return e.fail(err)
		}
		return e.fail(e.drive())
	}
	e.name = name
	e.started = true
	if err := e.publishInstructions(body, rosterNames(e.roster)); err != nil {
		return e.fail(err)
	}
	return e.fail(e.drive())
}

func (e *engine) mint(bare, id string) (string, error) {
	if err := checkName(bare); err != nil && len(bare) > bus.MaxNameLen {
		return "", nameLimit(bare)
	}
	if err := checkName(bare); err != nil {
		return "", err
	}
	taken, err := e.taken()
	if err != nil {
		return "", err
	}
	name := bare
	if taken[name] {
		name, err = e.suffixed(bare, taken)
		if err != nil {
			return "", err
		}
	}
	ticket := ""
	if e.cfg.TicketSet {
		ticket = e.cfg.Ticket
	}
	prompts := e.promptList()
	custom := ""
	if e.cfg.InstructionsSet {
		custom = e.cfg.Instructions
	}
	for {
		res, err := e.create(name, id, ticket, e.seat, e.dir, prompts, custom)
		if err != nil {
			return "", err
		}
		if res.AlreadyExisted == nil || !*res.AlreadyExisted {
			return name, nil
		}
		taken[name] = true
		name, err = e.suffixed(bare, taken)
		if err != nil {
			return "", err
		}
	}
}

func (e *engine) suffixed(bare string, taken map[string]bool) (string, error) {
	if len(bare)+1+conversationCode > bus.MaxNameLen {
		return "", nameLimit(bare + "-" + strings.Repeat("a", conversationCode))
	}
	for i := 0; i < 64; i++ {
		code, err := mintCode(e.cfg.Random)
		if err != nil {
			return "", err
		}
		name := bare + "-" + code
		if !taken[name] {
			return name, nil
		}
	}
	return "", errors.New("conversation name is taken")
}

func (e *engine) agree(conv client.Conversation) error {
	if e.cfg.TaskSet {
		id, err := e.taskID(e.cfg.Task)
		if err != nil {
			return err
		}
		if id != conv.Task {
			return errors.New("passed task does not match the stored task")
		}
	}
	if e.cfg.AgentsSet {
		names := rosterNames(e.roster)
		if !sameStrings(names, conv.Roster) {
			return errors.New("passed roster does not match the stored roster")
		}
	}
	if e.cfg.SeatSet && e.cfg.Seat != conv.Seat {
		return errors.New("passed seat does not match the stored seat")
	}
	if e.cfg.TicketSet && e.cfg.Ticket != conv.Ticket {
		return errors.New("passed ticket does not match the stored ticket")
	}
	if e.cfg.PromptsSet && !sameStrings(e.promptList(), conv.Prompts) {
		return errors.New("passed prompts do not match the stored prompts")
	}
	if e.cfg.InstructionsSet && e.cfg.Instructions != conv.Custom {
		return errors.New("passed instructions do not match the stored instructions")
	}
	return nil
}

func (e *engine) ensureRoster(stored []string) error {
	if e.cfg.AgentsSet {
		return nil
	}
	e.roster = make([]agent, len(stored))
	for i, name := range stored {
		e.roster[i] = agent{name: name}
	}
	return nil
}

// requireMinimum reads the task's min_members and refuses a roster smaller than it.
func (e *engine) requireMinimum(task string) error {
	n, err := e.minMembers(task)
	if err != nil {
		return err
	}
	if len(e.roster) < n {
		return fmt.Errorf("roster has %d members and task %s requires %d", len(e.roster), task, n)
	}
	e.minimum = n
	return nil
}

// canLaunch is false on a resume that stored names only, so it watches and does not start a process.
func (e *engine) canLaunch() bool {
	if len(e.roster) == 0 {
		return false
	}
	for _, a := range e.roster {
		if a.module == "" || a.model == "" {
			return false
		}
	}
	return true
}

func (e *engine) promptList() []string {
	if !e.cfg.PromptsSet {
		return nil
	}
	return strings.Fields(e.cfg.Prompts)
}

func (e *engine) fail(err error) error {
	if err == nil {
		return nil
	}
	if !e.outcome && e.started {
		e.out.line("outcome", "error")
		e.outcome = true
	}
	return err
}

func (e *engine) call(ctx context.Context, op string, body any) (client.Result, error) {
	res, err := client.Do(ctx, e.addr, op, e.token, body)
	if err != nil {
		return res, err
	}
	if res.Outcome == client.OutcomeConnectionFailure {
		return res, errors.New(res.Reason)
	}
	return res, nil
}

func (e *engine) find(name string) (client.Conversation, bool, error) {
	res, err := e.call(e.root, "list", struct{}{})
	if err != nil {
		return client.Conversation{}, false, err
	}
	if res.Outcome != bus.OutcomeOK {
		return client.Conversation{}, false, fmt.Errorf("list: %s", res.Reason)
	}
	if res.Conversations == nil {
		return client.Conversation{}, false, nil
	}
	for _, conv := range *res.Conversations {
		if conv.Name == name {
			return conv, true, nil
		}
	}
	return client.Conversation{}, false, nil
}

func (e *engine) taken() (map[string]bool, error) {
	res, err := e.call(e.root, "list", struct{}{})
	if err != nil {
		return nil, err
	}
	if res.Outcome != bus.OutcomeOK {
		return nil, fmt.Errorf("list: %s", res.Reason)
	}
	names := make(map[string]bool)
	if res.Conversations != nil {
		for _, conv := range *res.Conversations {
			names[conv.Name] = true
		}
	}
	return names, nil
}

func (e *engine) create(name, task, ticket, seat, dir string, prompts []string, custom string) (client.Result, error) {
	res, err := e.call(e.root, "create", client.CreateRequest{
		Name:      name,
		Task:      task,
		Ticket:    ticket,
		Seat:      seat,
		Directory: dir,
		Prompts:   prompts,
		Custom:    custom,
		Roster:    rosterNames(e.roster),
	})
	if err != nil {
		return res, err
	}
	if res.Outcome != bus.OutcomeOK {
		return res, fmt.Errorf("create: %s", res.Reason)
	}
	return res, nil
}

func (e *engine) publishInstructions(body string, to []string) error {
	res, err := e.call(e.root, "publish", client.PublishRequest{
		Conversation: e.name,
		From:         fromOwner,
		To:           to,
		Body:         body,
		TxID:         txInstructions,
		Kind:         kindInstructions,
	})
	if err != nil {
		return err
	}
	if res.Outcome != bus.OutcomeOK {
		return fmt.Errorf("publish instructions: %s", res.Reason)
	}
	return nil
}
