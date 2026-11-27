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
	"log/slog"
	"os"
	"path/filepath"
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

	kindPropose   = "propose"
	kindDismiss   = "dismiss"
	kindAgree     = "agree"
	kindObject    = "object"
	kindStand     = "stand"
	kindPass      = "pass"
	kindExtend    = "extend"
	kindDeadlock  = "deadlock"
	kindFixed     = "fixed"
	kindAggregate = "aggregate"
	// kindSettled is run's announcement of a settle to owner. Nothing reads it as state.
	kindSettled = "settled"

	roleMember = "hotseat/member"
	contextBus = "hotseat/bus"

	envAddress   = "HOTSEAT_ADDRESS"
	envTokenFile = "HOTSEAT_TOKEN_FILE"

	statusLaunched = "launched"
	statusJoined   = "joined"
	statusRunning  = "running"
	statusExited   = "exited"
	statusTimedOut = "timed-out"

	classProcessExit    = "process exit"
	classProcessLimit   = "process limit"
	classMissingSession = "missing session id"
	classBadTurn        = "bad turn"
	classTurnCap        = "turn cap"
	classNoBackup       = "no backup"
	classBelowMinimum   = "below minimum"
	classLaunchCap      = "launch cap"
	classNoReply        = "no reply"

	classBadDone      = "bad done"
	classBadFinding   = "bad finding"
	classNoAggregate  = "no aggregate"
	classBadAggregate = "bad aggregate"
	classNoStance     = "no stance"
	classExtraStance  = "extra stance"
	classBadStance    = "bad stance"
	classBadPass      = "bad pass"
	classBadExtend    = "bad extend"
	classBadDeadlock  = "bad deadlock"
	classNoDecision   = "no decision"
	classNoFix        = "no fix"
	classNoReport     = "no report"
	classUnsettled    = "unsettled"
)

// stanceKinds are the kinds a discussion launch sends one of. Prompts read them with findings on an unnamed read.
var stanceKinds = []string{kindPropose, kindDismiss, kindAgree, kindObject, kindStand}

// New creates a chat from setup, stores the setup with it, and drives it. Continue drives an existing chat.
// Each reports to report: the header once the chat and its lease are held, and the result at exit. A run that stops
// before its chat is bound reports nothing, and one that stops after it but before the lease reports Unheld. Event
// lines go to stderr only when cfg.Verbose is set. The token value and the lease holder are not reported, nor written
// to stderr, a prompt, or a member's environment. A run holds the chat lease from before it acts on the chat until it
// exits, and stops when the lease is lost.
func New(ctx context.Context, cfg Config, setup Setup, report Reporter, stderr io.Writer) error {
	return executePaced(ctx, cfg, defaultPace(), report, stderr, func(e *engine) error { return e.createChat(setup) })
}

// Continue drives the existing chat name from the setup New stored with it. It refuses a chat that does not exist,
// and one whose stored setup cannot launch members, before it takes the lease or writes anything.
func Continue(ctx context.Context, cfg Config, name string, report Reporter, stderr io.Writer) error {
	return executePaced(ctx, cfg, defaultPace(), report, stderr, func(e *engine) error { return e.continueChat(name) })
}

// executePaced builds an engine with its timing and runs act on it. Only tests pass a pace other than defaultPace.
func executePaced(ctx context.Context, cfg Config, p pace, report Reporter, stderr io.Writer, act func(*engine) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if report == nil {
		report = discardReporter{}
	}
	if stderr == nil {
		stderr = io.Discard
	}
	e, err := newEngine(ctx, cfg, p, report, stderr)
	if err != nil {
		return err
	}
	defer e.loseHeld(nil)
	defer e.cancel()
	defer e.endLease()
	return act(e)
}

type engine struct {
	root   context.Context
	child  context.Context
	cancel context.CancelFunc
	// held ends with cause errLeaseLost when the lease is lost, or when root ends.
	held     context.Context
	loseHeld context.CancelCauseFunc

	cfg   Config
	addr  string
	token string
	// memberEnv is the environment of every member process.
	memberEnv []string
	perm      string
	limit     time.Duration
	pace      pace
	maxTurns  int
	maxLaunch int
	maxSays   int
	dir       string
	startBin  string
	tkBin     string
	stderr    io.Writer
	reporter  Reporter
	events    *slog.Logger
	clock     func() time.Time
	// began is when this run started, for elapsed.
	began time.Time

	// holder is this run's lease holder id. It fences every conductor write and send.
	holder string
	// leaseEnd stops the lease renewal and waits for it. It is set once the lease is acquired.
	leaseEnd func()
	// leased is set once the lease is acquired, before any goroutine starts. Only then is a stop said to owner.
	leased bool

	roster []agent
	seat   string
	// minimum is the task's min_members. It is set before any launch, and zero adds no check.
	minimum int

	name       string
	binding    client.Chat
	continuing bool
	// taskSeq is the instructions message this run sent, which boot launches concern. It is set before any launch.
	taskSeq int64

	// rule serialises stop rulings so each done is judged once. It is taken before mu, never under it.
	rule     sync.Mutex
	mu       sync.Mutex
	status   map[string]string
	sessions map[string]string
	slots    map[string]*slot
	acted    int
	haltErr  error
	// haltClass is the stop class of haltErr, worked out once for its stop error and the result block.
	haltClass string
	// stopRes is the stop result reportStop read, or nil. Only the goroutine running execute touches it.
	stopRes *StopResult
	// say is held by noteHalt from recording the first halt until its stop error is sent, so no caller cancels before it.
	// It is taken before mu.
	say sync.Mutex
	// closed is set once the stopping done closed the chat.
	closed   bool
	closing  bool
	doneFrom string
	// doneSeq is the done the stop rule decided on, or zero. It is set before finishDone stores the stop record.
	doneSeq int64
	// ruledOut holds every done a stored bad done names. stops never judges one of them again.
	ruledOut map[int64]bool
	handoff  handoff
	launches int
	// inFlight counts member processes this run started or watches and has not yet recorded.
	inFlight  int
	windClass string
	// windSeq is the error windClass sent, or zero until it is stored.
	windSeq int64
	// idle is closed once windClass has its stored error and inFlight is zero.
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
	// recs holds the decision records read at start and each one a write returned since.
	recs records
}

type slot struct {
	mu sync.Mutex
}

func newEngine(ctx context.Context, cfg Config, p pace, report Reporter, stderr io.Writer) (*engine, error) {
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
	if cfg.TimeoutSet && cfg.Timeout < 0 {
		return nil, errors.New("timeout must not be negative")
	}
	addr := cfg.Address
	if addr == "" {
		addr = bus.DefaultListen
	}
	var token, tokenFile string
	if cfg.TokenFileSet || cfg.TokenFile != "" {
		// A member starts in the chat's directory, so a relative path would name another file there.
		// An empty path is no file. Joining it would name the working directory.
		tokenFile = cfg.TokenFile
		if tokenFile != "" && !filepath.IsAbs(tokenFile) {
			tokenFile = filepath.Join(cfg.Dir, tokenFile)
		}
		tok, err := bus.ReadTokenFile(tokenFile)
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
	holder, err := mintHolder()
	if err != nil {
		return nil, err
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	locked := &lockedWriter{w: stderr}
	var eventOut io.Writer
	if cfg.Verbose {
		eventOut = locked
	}
	child, cancel := context.WithCancel(ctx)
	held, loseHeld := context.WithCancelCause(ctx)
	e := &engine{
		root:     ctx,
		child:    child,
		cancel:   cancel,
		held:     held,
		loseHeld: loseHeld,
		cfg:      cfg,
		addr:     addr,
		token:    token,
		holder:   holder,

		memberEnv:    memberEnv(os.Environ(), addr, tokenFile),
		limit:        cfg.timeout(),
		pace:         p,
		maxTurns:     cfg.maxTurns(),
		maxLaunch:    cfg.maxLaunches(),
		maxSays:      cfg.maxSays(),
		startBin:     startBin,
		tkBin:        tkBin,
		stderr:       locked,
		reporter:     report,
		events:       newEventLog(eventOut),
		clock:        clock,
		began:        clock(),
		status:       make(map[string]string),
		sessions:     make(map[string]string),
		slots:        make(map[string]*slot),
		idle:         make(chan struct{}),
		judged:       make(map[int64]bool),
		ruledOut:     make(map[int64]bool),
		leftovers:    make(chan struct{}),
		leftoverEnds: make(map[string]int64),
	}
	return e, nil
}

// createChat validates setup, creates the chat with it stored, sends the task, and drives the chat.
func (e *engine) createChat(setup Setup) error {
	if strings.TrimSpace(setup.Task) == "" {
		return errors.New("a task is required")
	}
	roster, err := parseAgents(setup.Agents)
	if err != nil {
		return err
	}
	if len(roster) == 0 {
		return errors.New("a roster is required")
	}
	e.roster = roster
	seat := setup.Seat
	if seat == "" {
		seat = defaultSeat(e.roster)
	} else if seat != strings.ToLower(seat) || !validName(seat) {
		return fmt.Errorf("seat %s must be a lowercase hotseat name", seat)
	} else if !nameIn(e.roster, seat) {
		return fmt.Errorf("seat %s is not in the roster", seat)
	}
	e.seat = seat
	e.perm = setup.Permission
	if e.perm == "" {
		e.perm = DefaultPermission
	}
	if err := e.checkPermissions(); err != nil {
		return err
	}
	id, err := e.taskID(setup.Task)
	if err != nil {
		return err
	}
	if err := e.requireMinimum(id); err != nil {
		return err
	}
	dir, err := e.launchDirectory(setup.Ticket)
	if err != nil {
		return err
	}
	e.dir = dir
	prompts := strings.Fields(setup.Prompts)
	text, err := e.moduleText(id)
	if err != nil {
		return err
	}
	body, err := fitInstructions(instructionBody(text, seat, setup.Ticket, prompts, setup.Instructions))
	if err != nil {
		return err
	}
	slug, err := taskSlug(id)
	if err != nil {
		return err
	}
	e.binding = client.Chat{
		Task: id, Ticket: setup.Ticket, Seat: seat, Directory: dir, Prompts: prompts, Custom: setup.Instructions,
		Roster: e.rosterEntries(), Permission: e.perm,
	}
	name, err := e.mint(bareChat(slug, setup.Ticket))
	if err != nil {
		return err
	}
	e.name = name
	e.binding.Name = name
	return e.finish(e.begin(body))
}

// continueChat drives the stored chat name with the roster, seat, and permission stored with it.
func (e *engine) continueChat(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	chat, ok, err := e.find(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("chat %s does not exist", name)
	}
	if err := launchable(chat); err != nil {
		return err
	}
	e.roster = make([]agent, len(chat.Roster))
	for i, r := range chat.Roster {
		e.roster[i] = agent{name: r.Name, module: r.Module, model: r.Model}
	}
	e.perm = chat.Permission
	e.binding = chat
	e.name = chat.Name
	e.seat = chat.Seat
	e.dir = chat.Directory
	e.continuing = true
	if err := e.acquire(true); err != nil {
		return e.finish(err)
	}
	e.reporter.Header(e.header())
	if err := e.ensureInstructions(chat); err != nil {
		return e.finish(err)
	}
	if err := e.loadMembers(); err != nil {
		return e.finish(err)
	}
	return e.finish(e.drive())
}

// launchable refuses a stored setup run cannot launch members from, naming each part that is missing.
func launchable(chat client.Chat) error {
	var missing []string
	if chat.Task == "" {
		missing = append(missing, "no task")
	}
	if chat.Seat == "" {
		missing = append(missing, "no seat")
	}
	if len(chat.Roster) == 0 {
		missing = append(missing, "no roster")
	}
	var noModule, noModel, neither []string
	for _, r := range chat.Roster {
		switch {
		case r.Module == "" && r.Model == "":
			neither = append(neither, r.Name)
		case r.Module == "":
			noModule = append(noModule, r.Name)
		case r.Model == "":
			noModel = append(noModel, r.Name)
		}
	}
	if len(noModule) > 0 {
		missing = append(missing, "no module for "+strings.Join(noModule, ", "))
	}
	if len(noModel) > 0 {
		missing = append(missing, "no model for "+strings.Join(noModel, ", "))
	}
	if len(neither) > 0 {
		missing = append(missing, "no module and model for "+strings.Join(neither, ", "))
	}
	if chat.Permission == "" {
		missing = append(missing, "no permission")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("chat %s has an incomplete setup: %s; only a chat run new created can continue", chat.Name, strings.Join(missing, "; "))
}

// begin takes the lease on a chat this run created, sends its task, and drives it.
// A new chat has no conductor, so the acquire does not wait.
func (e *engine) begin(body string) error {
	if err := e.acquire(false); err != nil {
		return err
	}
	e.reporter.Header(e.header())
	seq, err := e.sendInstructions(body, rosterNames(e.roster))
	if err != nil {
		return err
	}
	e.taskSeq = seq
	return e.drive()
}

// mint creates the chat under bare, or under bare and a code when bare is taken, with e.binding stored.
func (e *engine) mint(bare string) (string, error) {
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
	for {
		res, err := e.create(name)
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
	if len(bare)+1+chatCode > bus.MaxNameLen {
		return "", nameLimit(bare + "-" + strings.Repeat("a", chatCode))
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
	return "", errors.New("chat name is taken")
}

// requireMinimum reads the task's min_members and refuses a roster smaller than it.
func (e *engine) requireMinimum(task string) error {
	n, err := e.minMembers(task)
	if err != nil {
		return err
	}
	if len(e.roster) < n {
		return &rosterRefusal{have: len(e.roster), task: task, want: n}
	}
	e.minimum = n
	return nil
}

// rosterEntries is the roster as the bus stores it.
func (e *engine) rosterEntries() []client.RosterEntry {
	out := make([]client.RosterEntry, len(e.roster))
	for i, a := range e.roster {
		out[i] = client.RosterEntry{Name: a.name, Module: a.module, Model: a.model}
	}
	return out
}

// finish ends a run that bound its chat and returns err. It is the stop point of an error stop that reached it
// without passing noteHalt, such as one before drive or a failed stop report. An unresolved stop is not an error stop.
// Before the lease is held it reports Unheld, and once it was held the result.
func (e *engine) finish(err error) error {
	if err != nil && !errors.Is(err, ErrUnresolved) {
		err = e.halt(err)
	}
	if !e.leased {
		if err != nil {
			e.reporter.Unheld()
		}
		return err
	}
	e.reporter.Result(e.result(err))
	return err
}

// header is the chat this run holds, its binding, and its roster.
func (e *engine) header() Header {
	h := Header{Chat: e.name, Task: e.binding.Task, Ticket: e.binding.Ticket, Seat: e.seat}
	for _, a := range e.roster {
		h.Members = append(h.Members, Member{Name: a.name, Module: a.module, Model: a.model})
	}
	return h
}

// result is how a run that held its lease and returns err ended: the stop result of the done the stop record names,
// the stop class of an error stop, the outcome, and the elapsed time. The outcome is done only for a nil err, the stop
// result's outcome for an unresolved stop, and error otherwise.
func (e *engine) result(err error) Result {
	r := Result{Stop: e.recordedStop(), Outcome: outcomeError, Elapsed: e.clock().Sub(e.began).Round(time.Second)}
	e.mu.Lock()
	r.StopClass = e.haltClass
	e.mu.Unlock()
	switch {
	case err == nil:
		r.Outcome = outcomeDone
	case errors.Is(err, ErrUnresolved) && r.Stop != nil:
		r.Outcome = r.Stop.Outcome
	}
	return r
}

// call makes one bus call. Any outcome but ok is a *busError, after a refusal for want of the lease stops the run.
func (e *engine) call(ctx context.Context, op string, body any) (client.Result, error) {
	res, err := client.Do(ctx, e.addr, op, e.token, body)
	if err != nil {
		return res, err
	}
	if err := e.haltOnLostLease(res); err != nil {
		return res, err
	}
	return res, outcomeErr(op, body, res)
}

// outcomeErr is the *busError of a call that answered other than ok, or nil.
func outcomeErr(op string, body any, res client.Result) error {
	if res.Outcome != bus.OutcomeOK {
		return &busError{op: callLabel(op, body), outcome: res.Outcome, reason: res.Reason}
	}
	return nil
}

// callLabel names a call in its error: the route, and the kind, member, or lease operation it concerns.
func callLabel(op string, body any) string {
	switch req := body.(type) {
	case client.SendRequest:
		return op + " " + req.Kind
	case client.MemberRequest:
		return op + " " + req.Name
	case client.SessionRequest:
		if req.Name != nil {
			return op + " " + *req.Name
		}
	case client.LeaseRequest:
		return op + " " + req.Lease
	}
	return op
}

func (e *engine) find(name string) (client.Chat, bool, error) {
	res, err := e.call(e.root, "list", struct{}{})
	if err != nil {
		return client.Chat{}, false, err
	}
	chat, ok := pickBinding(res, name)
	return chat, ok, nil
}

func (e *engine) taken() (map[string]bool, error) {
	res, err := e.call(e.root, "list", struct{}{})
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool)
	if res.Chats != nil {
		for _, chat := range *res.Chats {
			names[chat.Name] = true
		}
	}
	return names, nil
}

// create creates the chat name with e.binding stored.
func (e *engine) create(name string) (client.Result, error) {
	b := e.binding
	return e.call(e.root, "create", client.CreateRequest{
		Name:       name,
		Task:       b.Task,
		Ticket:     b.Ticket,
		Seat:       b.Seat,
		Directory:  b.Directory,
		Prompts:    b.Prompts,
		Custom:     b.Custom,
		Roster:     b.Roster,
		Permission: b.Permission,
	})
}

// sendInstructions sends the task as owner and returns its seq.
func (e *engine) sendInstructions(body string, to []string) (int64, error) {
	seq, _, err := e.send(post{kind: kindInstructions, from: fromOwner, to: to, body: body, tx: txInstructions})
	return seq, err
}
