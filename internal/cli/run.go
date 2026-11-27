// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/run"
	"github.com/p3bot/hotseat/internal/web"
)

const runLong = `Drive one task's chat on a running bus. run new creates the chat and drives
it, and run continue resumes a chat run new created. Members launch through
start, and run exits when the seat sends done or a limit trips.

stdout is a header (chat, task, seat, members, page) and, at exit, a result
that ends with outcome and elapsed. outcome: done exits 0. deadlock,
unsettled, and error exit 1, and a signal exits 128 plus its number.
--verbose writes run's events to stderr.

` + referenceLine + "docs/run.md"

const runNewLong = `Create a chat for task and drive it. task is a module query that start
resolves, such as hotseat/ticket/review. --agents is required: a
space-separated roster of name=module:model.

The chat name is the ticket id, a hyphen, and the task slug, or the slug
alone with no ticket. A short code is added when the name is taken. It is
the first header line, chat: <name>. run new stores the setup with the chat,
so run continue needs only that name.

` + referenceLine + "docs/run.md"

const runContinueLong = `Resume a chat run new created, from the setup stored with it. continue takes
no setup flag, so a different roster, seat, or permission needs run new. It
adopts members still running from an earlier run and resumes from the
transcript and the decision records. A missing chat, or one that hotseat chat
create made, is refused before anything is written.

` + referenceLine + "docs/run.md"

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "run",
		Short:         "Drive one chat on a bus that is already up",
		Long:          runLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("choose new or continue")
		},
	}
	cmd.PersistentFlags().String(flagAddress, bus.DefaultListen, "bus address (env "+envAddress+")")
	cmd.PersistentFlags().String(flagTokenFile, "", "file holding the capability token; the token is not a flag (env "+envTokenFile+")")
	cmd.AddCommand(newRunNewCmd(), newRunContinueCmd())
	return cmd
}

func newRunNewCmd() *cobra.Command {
	var f runFlags
	var s setupFlags
	cmd := &cobra.Command{
		Use:           "new <task>",
		Short:         "Create a chat for a task and drive it",
		Long:          runNewLong,
		Example:       `  hotseat run new hotseat/ticket/review --agents "alpha=hotseat/grok:model bravo=hotseat/claude:model" --ticket foo-x234`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          chatArgs("task"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := f.config(cmd)
			if err != nil {
				return err
			}
			setup, err := s.setup(cmd, args[0])
			if err != nil {
				return err
			}
			return driveRun(cmd, cfg, func(report run.Reporter) error {
				return run.New(cmd.Context(), cfg, setup, report, cmd.ErrOrStderr())
			})
		},
	}
	s.add(cmd)
	f.add(cmd)
	return cmd
}

func newRunContinueCmd() *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:           "continue <name>",
		Short:         "Drive a chat run new created, from its stored setup",
		Long:          runContinueLong,
		Example:       `  hotseat run continue foo-x234-ticket-review`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          chatArgs("name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := f.config(cmd)
			if err != nil {
				return err
			}
			return driveRun(cmd, cfg, func(report run.Reporter) error {
				return run.Continue(cmd.Context(), cfg, args[0], report, cmd.ErrOrStderr())
			})
		},
	}
	f.add(cmd)
	return cmd
}

// driveRun runs one verb's engine call with a report on stdout and maps its error to the run exit rule.
func driveRun(cmd *cobra.Command, cfg run.Config, call func(run.Reporter) error) error {
	report := &runReport{lw: lineWriter{w: cmd.OutOrStdout()}, page: runPage(cfg.Address)}
	err := call(report)
	if err == nil {
		err = report.lw.err
	}
	return runExit(err)
}

// runExit makes an unresolved stop exit 1 with no stderr line. Its report is already on stdout.
func runExit(err error) error {
	if errors.Is(err, run.ErrUnresolved) {
		return reportedExit{code: ExitFailure}
	}
	return err
}

// runFlags are the limit flags both run verbs take. None is stored with the chat.
type runFlags struct {
	timeout     time.Duration
	maxTurns    int
	maxLaunches int
	maxSays     int
	verbose     bool
}

func (f *runFlags) add(cmd *cobra.Command) {
	cmd.Flags().DurationVar(&f.timeout, "timeout", run.DefaultProcessLimit, "limit for one member process; 0 is no limit (env HOTSEAT_TIMEOUT)")
	cmd.Flags().IntVar(&f.maxTurns, "max-turns", run.DefaultMaxTurns, "turn messages this process acts on (env HOTSEAT_MAX_TURNS)")
	cmd.Flags().IntVar(&f.maxLaunches, "max-launches", run.DefaultMaxLaunches, "member processes this process starts (env HOTSEAT_MAX_LAUNCHES)")
	cmd.Flags().IntVar(&f.maxSays, "max-says", run.DefaultMaxSays, "discussion launches per member per finding (env HOTSEAT_MAX_SAYS)")
	cmd.Flags().BoolVar(&f.verbose, "verbose", false, "write event lines and successful start and tk stderr to stderr (env HOTSEAT_VERBOSE)")
}

func (f runFlags) config(cmd *cobra.Command) (run.Config, error) {
	dir, err := os.Getwd()
	if err != nil {
		return run.Config{}, err
	}
	cfg := run.Config{Dir: dir}
	if cfg.Address, err = resolveAddress(cmd); err != nil {
		return run.Config{}, err
	}
	if cfg.TokenFile, cfg.TokenFileSet, err = resolveTokenFile(cmd); err != nil {
		return run.Config{}, err
	}
	cfg.Timeout, cfg.TimeoutSet, err = pickDuration(cmd, "timeout", "HOTSEAT_TIMEOUT", f.timeout)
	if err != nil {
		return run.Config{}, err
	}
	cfg.MaxTurns, cfg.MaxTurnsSet, err = pickInt(cmd, "max-turns", "HOTSEAT_MAX_TURNS", f.maxTurns)
	if err != nil {
		return run.Config{}, err
	}
	cfg.MaxLaunches, cfg.MaxLaunchesSet, err = pickInt(cmd, "max-launches", "HOTSEAT_MAX_LAUNCHES", f.maxLaunches)
	if err != nil {
		return run.Config{}, err
	}
	cfg.MaxSays, cfg.MaxSaysSet, err = pickInt(cmd, "max-says", "HOTSEAT_MAX_SAYS", f.maxSays)
	if err != nil {
		return run.Config{}, err
	}
	cfg.Verbose, _ = pickBool(cmd, "verbose", "HOTSEAT_VERBOSE", f.verbose)
	return cfg, nil
}

// setupFlags are the flags run new stores with the chat.
type setupFlags struct {
	agents       string
	seat         string
	ticket       string
	prompts      string
	instructions string
	permission   string
}

func (s *setupFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&s.agents, "agents", "", "roster as name=module:model, space-separated (env HOTSEAT_AGENTS)")
	cmd.Flags().StringVar(&s.seat, "seat", "", "seat name (env HOTSEAT_SEAT); default is the first roster name in ASCII order")
	cmd.Flags().StringVar(&s.ticket, "ticket", "", "ticket id scope-short (env HOTSEAT_TICKET)")
	cmd.Flags().StringVar(&s.prompts, "prompts", "", "space-separated module addresses (env HOTSEAT_PROMPTS)")
	cmd.Flags().StringVar(&s.instructions, "instructions", "", "custom instruction text (env HOTSEAT_INSTRUCTIONS)")
	cmd.Flags().StringVar(&s.permission, "permission", run.DefaultPermission, "permission stored for every member launch (env HOTSEAT_PERMISSION)")
}

// setup is the run.Setup the flags and their variables name. An explicit empty --seat or --permission is an error,
// so a passed flag is not replaced by the default and then stored with the chat.
func (s setupFlags) setup(cmd *cobra.Command, task string) (run.Setup, error) {
	for _, flag := range []struct{ name, value string }{{"seat", s.seat}, {"permission", s.permission}} {
		if cmd.Flags().Changed(flag.name) && flag.value == "" {
			return run.Setup{}, errors.New("--" + flag.name + " is empty")
		}
	}
	out := run.Setup{Task: task}
	for _, f := range []struct {
		flag, env, value string
		dst              *string
	}{
		{"agents", "HOTSEAT_AGENTS", s.agents, &out.Agents},
		{"seat", "HOTSEAT_SEAT", s.seat, &out.Seat},
		{"ticket", "HOTSEAT_TICKET", s.ticket, &out.Ticket},
		{"prompts", "HOTSEAT_PROMPTS", s.prompts, &out.Prompts},
		{"instructions", "HOTSEAT_INSTRUCTIONS", s.instructions, &out.Instructions},
		{"permission", "HOTSEAT_PERMISSION", s.permission, &out.Permission},
	} {
		*f.dst = pickString(cmd, f.flag, f.env, f.value)
	}
	return out, nil
}

// runPage is the page address for a bus at busAddr when its host is a loopback IP or localhost, and empty otherwise,
// because the page listens on loopback on the bus host only. It neither resolves nor validates the address: the
// page line is informational, and a bad address fails at the first bus call as on a chat verb.
func runPage(busAddr string) string {
	host, _, err := net.SplitHostPort(busAddr)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); (ip == nil || !ip.IsLoopback()) && !strings.EqualFold(host, "localhost") {
		return ""
	}
	addr, err := pageListen(busAddr)
	if err != nil {
		return ""
	}
	return addr
}

// runReport prints a run's header and result as text lines on stdout. A failed write is kept in lw for after the run,
// and the run is not told: a failed stdout write stops nothing and sends no stop error.
type runReport struct {
	lw lineWriter
	// page is the page address for the chat view, or empty for no page line.
	page string
}

func (r *runReport) Unheld() {
	r.lw.plain("outcome", "error")
}

func (r *runReport) Header(h run.Header) {
	r.lw.plain("chat", h.Chat)
	r.lw.plain("task", h.Task)
	if h.Ticket != "" {
		r.lw.plain("ticket", h.Ticket)
	}
	r.lw.plain("seat", h.Seat)
	for _, m := range h.Members {
		r.lw.plain("member", m.Name+"="+m.Module+":"+m.Model)
	}
	if r.page != "" {
		r.lw.plain("page", web.ChatURL(r.page, h.Chat))
	}
}

func (r *runReport) Result(res run.Result) {
	if res.Stop != nil {
		r.lw.stopLines(res.Stop)
	}
	if res.StopClass != "" {
		r.lw.plain("stop", res.StopClass)
	}
	r.lw.plain("outcome", res.Outcome)
	r.lw.plain("elapsed", res.Elapsed.String())
}

// pickString is the flag when passed, else its environment variable when set, else empty.
func pickString(cmd *cobra.Command, flag, env, value string) string {
	if cmd.Flags().Changed(flag) {
		return value
	}
	return os.Getenv(env)
}

func pickDuration(cmd *cobra.Command, flag, env string, value time.Duration) (time.Duration, bool, error) {
	if cmd.Flags().Changed(flag) {
		return value, true, nil
	}
	raw, ok := os.LookupEnv(env)
	if !ok || raw == "" {
		return value, false, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, false, err
	}
	return d, true, nil
}

func pickInt(cmd *cobra.Command, flag, env string, value int) (int, bool, error) {
	if cmd.Flags().Changed(flag) {
		return value, true, nil
	}
	raw, ok := os.LookupEnv(env)
	if !ok || raw == "" {
		return value, false, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
}

func pickBool(cmd *cobra.Command, flag, env string, value bool) (bool, bool) {
	if cmd.Flags().Changed(flag) {
		return value, true
	}
	raw, ok := os.LookupEnv(env)
	if !ok || raw == "" {
		return false, false
	}
	return true, true
}
