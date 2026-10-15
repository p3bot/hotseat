// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/run"
)

func newRunCmd() *cobra.Command {
	var f runFlags
	cmd := &cobra.Command{
		Use:   "run [task]",
		Short: "Run one conversation on a bus that is already up",
		Long: `Run one conversation on a bus that is already listening. run does not start
the bus and it does not open the database. It publishes the task as owner,
launches each roster member through start, and exits when the acting seat
posts done or a safety rail trips.

The roster is --agents or HOTSEAT_AGENTS, a space-separated list of
name=module:model. There is no HOTSEAT_MEMBERS. The flag wins over the
matching HOTSEAT_* variable. --seat or HOTSEAT_SEAT names the seat. When both
are absent, the seat is the first roster name in ASCII byte order.

--max-turns or HOTSEAT_MAX_TURNS defaults to 500 turn messages. --timeout or
HOTSEAT_TIMEOUT limits one member process. The default is 8h. It is not a
limit on the conversation. --interactive or a non-empty HOTSEAT_INTERACTIVE
disables that process limit and leaves the turn cap. --permission or
HOTSEAT_PERMISSION defaults to auto.

--ticket or HOTSEAT_TICKET selects the code-root members launch in.
--conversation or HOTSEAT_CONVERSATION continues that exact conversation.
A resume launches a member only when --agents or HOTSEAT_AGENTS is passed
again. Without that roster, the resume continues and does not launch.
--prompts or HOTSEAT_PROMPTS and --instructions or HOTSEAT_INSTRUCTIONS are
stored on the binding. --address and --token-file follow the other client
commands. A non-default address or a token path is passed only in the member
launch prompt. The token value is not printed.

A second run of the same task creates a new conversation unless --conversation
names the existing one. agy (Antigravity CLI) has no hotseat module. Pass the
task query when creating. When --conversation names a conversation that is
not on the bus, the task argument is required.`,
		Example: `  hotseat run review/ticket --agents "alpha=hotseat/grok:model bravo=hotseat/claude:model" --ticket foo-x234
  hotseat run --conversation review-ticket-foo-x234 --agents "alpha=hotseat/grok:model bravo=hotseat/claude:model"`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := f.config(cmd, args)
			if err != nil {
				return err
			}
			return run.Execute(cmd.Context(), cfg, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&f.agents, "agents", "", "roster as name=module:model, space-separated (env HOTSEAT_AGENTS)")
	cmd.Flags().StringVar(&f.seat, "seat", "", "seat name (env HOTSEAT_SEAT); default is the first roster name in ASCII order")
	cmd.Flags().StringVar(&f.prompts, "prompts", "", "space-separated module addresses (env HOTSEAT_PROMPTS)")
	cmd.Flags().StringVar(&f.instructions, "instructions", "", "custom instruction text (env HOTSEAT_INSTRUCTIONS)")
	cmd.Flags().DurationVar(&f.timeout, "timeout", run.DefaultProcessLimit, "limit for one member process (env HOTSEAT_TIMEOUT)")
	cmd.Flags().IntVar(&f.maxTurns, "max-turns", run.DefaultMaxTurns, "turn messages this process acts on (env HOTSEAT_MAX_TURNS)")
	cmd.Flags().BoolVar(&f.interactive, "interactive", false, "disable the process limit and keep the turn cap (env HOTSEAT_INTERACTIVE)")
	cmd.Flags().StringVar(&f.ticket, "ticket", "", "ticket id scope-short (env HOTSEAT_TICKET)")
	cmd.Flags().StringVar(&f.conversation, "conversation", "", "exact conversation name to continue (env HOTSEAT_CONVERSATION)")
	cmd.Flags().StringVar(&f.permission, "permission", run.DefaultPermission, "permission passed to every member launch (env HOTSEAT_PERMISSION)")
	addAddress(cmd, &f.address)
	addTokenFile(cmd)
	return cmd
}

type runFlags struct {
	agents       string
	seat         string
	prompts      string
	instructions string
	timeout      time.Duration
	maxTurns     int
	interactive  bool
	ticket       string
	conversation string
	permission   string
	address      string
}

func (f runFlags) config(cmd *cobra.Command, args []string) (run.Config, error) {
	dir, err := os.Getwd()
	if err != nil {
		return run.Config{}, err
	}
	cfg := run.Config{Dir: dir, Address: f.address}
	if len(args) == 1 {
		cfg.Task = args[0]
		cfg.TaskSet = true
	}
	if cfg.Agents, cfg.AgentsSet, err = pickString(cmd, "agents", "HOTSEAT_AGENTS", f.agents); err != nil {
		return run.Config{}, err
	}
	if cfg.Seat, cfg.SeatSet, err = pickString(cmd, "seat", "HOTSEAT_SEAT", f.seat); err != nil {
		return run.Config{}, err
	}
	if cfg.Prompts, cfg.PromptsSet, err = pickString(cmd, "prompts", "HOTSEAT_PROMPTS", f.prompts); err != nil {
		return run.Config{}, err
	}
	if cfg.Instructions, cfg.InstructionsSet, err = pickString(cmd, "instructions", "HOTSEAT_INSTRUCTIONS", f.instructions); err != nil {
		return run.Config{}, err
	}
	if cfg.Ticket, cfg.TicketSet, err = pickString(cmd, "ticket", "HOTSEAT_TICKET", f.ticket); err != nil {
		return run.Config{}, err
	}
	if cfg.Conversation, cfg.ConversationSet, err = pickString(cmd, "conversation", "HOTSEAT_CONVERSATION", f.conversation); err != nil {
		return run.Config{}, err
	}
	if cfg.Permission, cfg.PermissionSet, err = pickString(cmd, "permission", "HOTSEAT_PERMISSION", f.permission); err != nil {
		return run.Config{}, err
	}
	if !cfg.PermissionSet && cfg.Permission == "" {
		cfg.Permission = run.DefaultPermission
	}
	cfg.Timeout, cfg.TimeoutSet, err = pickDuration(cmd, "timeout", "HOTSEAT_TIMEOUT", f.timeout)
	if err != nil {
		return run.Config{}, err
	}
	cfg.MaxTurns, cfg.MaxTurnsSet, err = pickInt(cmd, "max-turns", "HOTSEAT_MAX_TURNS", f.maxTurns)
	if err != nil {
		return run.Config{}, err
	}
	cfg.Interactive, cfg.InteractiveSet = pickBool(cmd, "interactive", "HOTSEAT_INTERACTIVE", f.interactive)
	if cmd.Flags().Changed("token-file") {
		path, err := cmd.Flags().GetString("token-file")
		if err != nil {
			return run.Config{}, err
		}
		cfg.TokenFile = path
		cfg.TokenFileSet = true
	} else if path, ok := os.LookupEnv("HOTSEAT_TOKEN_FILE"); ok && path != "" {
		cfg.TokenFile = path
		cfg.TokenFileSet = true
	}
	if !cmd.Flags().Changed("address") {
		if addr, ok := os.LookupEnv("HOTSEAT_ADDRESS"); ok && addr != "" {
			cfg.Address = addr
		}
	}
	if cfg.Address == "" {
		cfg.Address = bus.DefaultListen
	}
	return cfg, nil
}

func pickString(cmd *cobra.Command, flag, env, value string) (string, bool, error) {
	if cmd.Flags().Changed(flag) {
		return value, true, nil
	}
	raw, ok := os.LookupEnv(env)
	if !ok {
		return "", false, nil
	}
	return raw, true, nil
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
