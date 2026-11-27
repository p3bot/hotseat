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

const runLong = `Drive one chat on a bus that is already listening. run does not start the
bus and it does not open the database. run new creates a chat and drives it,
and run continue drives a chat run new created, from what the chat stores.
Both send the task as owner when the chat has none, launch each roster
member through start, and exit when the acting seat sends done or a safety
rail trips. The stopping done is the seat's, or the backup's while the seat
is timed-out. Any other done stored before the stop is reported once with
error class bad done and never stops the run afterwards, including on a
continuation. A done after the stop is not judged. run stores the stopping
done as a stop record before it closes the chat, and a continuation stops on
that done.

A turn launches each roster member on its to list that has not timed out,
unless that member has already answered it. Once that member's earlier
process has ended, run reads the transcript. A member that has sent a say or
finding after the turn whose re names that turn has answered and is not
launched for it, whatever its to list. One with no re has answered when its
to list names the turn's sender, and a to list of all does not count. A say
or finding whose re names another message, and an ask, do not answer. A live
run, a continuation, and the no reply report follow the same rule. Each turn
prompt gives --re with the turn's seq for the say and each finding.

Each member process runs start with the stored module, model, and permission.
Its environment is run's own with HOTSEAT_ADDRESS set to run's resolved bus
address, and HOTSEAT_TOKEN_FILE set to the token file as an absolute path
against run's working directory when one is set, because a member starts in
the chat's directory. The token itself goes into no environment, prompt, or
output, and prompts give chat command lines without --address or
--token-file. start passes its environment to the agent it execs.

--max-turns or HOTSEAT_MAX_TURNS defaults to 500 turn messages. --timeout or
HOTSEAT_TIMEOUT limits one member process. The default is 8h, and 0 means no
limit. It is not a limit on the chat. Only the run holding the lease enforces
it: a run that lost its lease or died enforces none, and a continuation gives
each process it adopts the full limit from adoption.

--max-launches or HOTSEAT_MAX_LAUNCHES defaults to 300 member processes in
this run process, of every launch kind. A continuation counts from zero. A
task's min_members, read through start, is the smallest roster run accepts.
A smaller roster creates nothing, and a continuation with a stored roster
below it exits with an error and leaves a process still running from an
earlier run alone. When a member times out and fewer than min_members are
not timed-out, or a launch would pass the cap, run sends error to owner with
class below minimum or launch cap and starts no further launch. Running
members finish and keep their session ids. run then exits non-zero with
outcome: error and the chat open. A stopping done still closes it. A stored
stopping done is reported before the minimum is read.

A finding from a recipient of a set that has not finished opens a discussion
when its re names that set's turn, or it has no re. Any other finding is
reported once with error class bad finding.
When the set's recipients have exited or timed out, run reports each live
recipient that did not reply with error class no reply, and writes the set
record. A set with two or more findings gets one acting-seat launch that
sends aggregate: one line per issue, the kept finding first and its dropped
duplicates after. run writes the set's grouping record with those drops, or
with none when the aggregate is missing or invalid. run then discusses each
kept finding one at a time, and then runs the follow-up, whose fixed: <n> is
the set's fix records. Members send one stance per discussion launch:
propose, dismiss, agree, object, or stand. A finding settles when every live
member but the proposer agrees. run writes the settle record and sends
settled to owner with re the agreed proposal, and the finder applies a
settled change and sends fixed. --max-says or HOTSEAT_MAX_SAYS defaults to 4
discussion launches per member per finding. When nobody can speak, the
acting seat sends extend or deadlock. A deadlock writes the halt record,
stops the run, and the seat reports with done.

run keeps these decisions as records in the store, keyed by message seq, and
sends no discuss, issues, or halted marker. run continue resumes from the
transcript and the records, so a dump of messages is not a full backup.

Each prompt gives the exact --re for the kind it asks for. A present re that
names another message rejects a stance, pass, aggregate, extend, or deadlock
under its class, and a fixed with one does not count. A missing re is never
an error. A protocol error carries the member and class in its body and the
message that caused it as re. bad finding, no reply, bad turn, bad done, and
unsettled take a transaction id derived from their fact, so a continuation
stores and prints none of them twice.

The stop result is read from the messages and records before the stopping
done. A settled change is fixed by its fix record, or by a fixed after the
settle whose re names the finding, from the finder, the seat, or its backup.
An unsettled finding prints unsettled, a settled change not fixed prints
unfixed, and run exits non-zero with outcome: deadlock after a deadlock or
outcome: unsettled otherwise. outcome: done is the only zero exit.

stdout is a header and a result block. Once the chat and its lease are held,
run prints chat, task, ticket unless empty, seat, one member line per
roster entry as name=module:model, and page, the chat view at the
bus port plus one, only when the --address host is a loopback IP or
localhost. At exit it prints done when a stop record exists, with that done's unsettled and
unfixed lines, then stop with the class of an error stop, then outcome, and
last elapsed rounded to seconds. done, unsettled, unfixed, and stop match
hotseat chat status. An exit before the chat is bound prints nothing on
stdout. An exit after that but before the lease is held, such as a refused
acquire, prints only outcome: error.

--verbose or a non-empty HOTSEAT_VERBOSE writes event lines to stderr, one
per line with a local time: each launch with its member, kind, and seq, each
process exit with its code and duration, each adopt of a process left by an
earlier run and its exit once gone, and each turn, issue, finding, stance,
pass, settled, stall, extend, deadlock, fixed, and error run stores to
owner, a member timeout and the stop error included. It also forwards a
successful start or tk call's stderr, each line prefixed with the tool name.
Without it neither is written. Exit statuses do not depend on it.

--address and --token-file resolve as on hotseat chat: the flag, else
HOTSEAT_ADDRESS or HOTSEAT_TOKEN_FILE, else the default address and no
token file.

run holds the chat's conductor lease while it drives, renews it every 5s,
and releases it at exit. A second run on a chat another run is driving waits
once, at most 20s, then exits non-zero with outcome: error and chat has a
conductor on stderr, having done nothing. A run that loses its lease stops
launching and writing, kills no member, waits for the members it launched
to exit, and exits non-zero with outcome: error. The next holder adopts and
records its members. A continuation adopts a member process only when its
host start token matches the one stored with its pid. The lease holder is
not printed.

A run that holds its lease and stops on an error sends one error from run to
owner before it waits for any member or releases its lease. The body is the
class alone: stopped by protocol, with re the halting protocol error (turn
cap, no backup, no decision, no fix, no report, launch cap, or below
minimum); stopped by refusal, a bus refusal for any reason but the lease;
stopped by command, a failed start or tk call or output run cannot use;
stopped by bus, unavailable or unreachable; stopped by lease; stopped by
signal; or stopped by run for any other failure. The first cause wins, and
after a signal the stop is stopped by signal. One member's failed launch in a
parallel turn stops the others at once. The detail stays on stderr. An
unresolved stop, a failed stdout write, an exit before the lease is held, and
a continuation's stored roster below min_members send none. A member run
stops is not a member failure, and a --max-turns stop exits non-zero with
outcome: error, as a launch-cap stop does.`

const runNewLong = `Create a chat for task and drive it. task is a module query start resolves to
a task id. run new stores the setup with the chat: the task, the roster, the
seat, the ticket, the prompts, the instructions, the permission, and the
directory members launch in. run continue launches from that stored setup.

The roster is --agents or HOTSEAT_AGENTS, a space-separated list of
name=module:model, and is required. --seat or HOTSEAT_SEAT names the seat.
When both are absent, the seat is the first roster name in ASCII byte order.
--ticket or HOTSEAT_TICKET selects the code-root members launch in.
--prompts or HOTSEAT_PROMPTS and --instructions or HOTSEAT_INSTRUCTIONS go
into the task message and are stored. --permission or HOTSEAT_PERMISSION
defaults to auto, and every roster module must define it. Each flag wins over
its HOTSEAT_* variable. An empty --seat or --permission is a usage error, and
an empty variable counts as unset. agy (Antigravity CLI) has no hotseat module.

The chat name is not a flag. It is the ticket id, a hyphen, and the task
slug. With no ticket the slug is the whole name. A taken name gains one
4-character code, so a second run of the same task creates a new chat. The
header prints chat: <name> before run blocks, and the chief reads the chat
name from that line to start hotseat chat attend <name>.`

const runContinueLong = `Drive the existing chat name from the setup run new stored with it: the task,
the roster with each member's module and model, the seat, the ticket, the
prompts, the instructions, the permission, and the directory. continue takes
no setup flag, so it cannot change the roster, seat, or permission. A
different setup needs run new.

continue refuses a chat that does not exist and creates nothing. It also
refuses a chat whose stored setup is incomplete: no task, no seat, no
roster, a roster entry with an empty module or model, or no permission, as a
chat that hotseat chat create made. That refusal names what is missing,
comes before the lease is acquired, and writes nothing to the chat.

A continuation sends the task message again only when the chat has none,
settles members left from an earlier run, adopts the live ones, and resumes
from the transcript and the decision records.`

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
