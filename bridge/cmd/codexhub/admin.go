// admin.go — codexhub's license-ops subcommands. They open the SQLite db
// directly (no hub API): simplest possible ops surface for the invite-only
// beta, and WAL makes it safe next to a running hub.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yunyuchen/codex-remote/bridge/internal/license"
)

func adminUsage() {
	fmt.Fprintln(os.Stderr, `usage: codexhub admin <command> [flags] [args]

  issue-key   [-db PATH] [-plan beta] [-machines 3] [-months 6]   mint a key (printed ONCE)
  list-keys   [-db PATH]                                          table of keys + usage
  revoke-key  [-db PATH] crk_...                                  bar a key (machines kicked within the hour)
  extend-key  [-db PATH] [-months 6] crk_...                      push expiry out
  unbind      [-db PATH] <machine-id>                             operator unbind (no swap limit)

-db defaults to $HUB_DB, then hub.db.`)
	os.Exit(2)
}

func dbFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("HUB_DB")
	if def == "" {
		def = "hub.db"
	}
	return fs.String("db", def, "license db path")
}

func openSvc(path string) *license.Service {
	svc, err := license.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin: open db:", err)
		os.Exit(1)
	}
	return svc
}

func adminFatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		os.Exit(1)
	}
}

func adminMain(args []string) {
	if len(args) == 0 {
		adminUsage()
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "issue-key":
		fs := flag.NewFlagSet("issue-key", flag.ExitOnError)
		db := dbFlag(fs)
		plan := fs.String("plan", "beta", "plan label")
		machines := fs.Int("machines", 3, "machine limit")
		months := fs.Int("months", 6, "validity in months")
		_ = fs.Parse(rest)
		svc := openSvc(*db)
		defer svc.Close()
		key, err := svc.IssueKey(*plan, *machines, *months)
		adminFatal(err)
		fmt.Println(key)
		fmt.Fprintln(os.Stderr, "shown ONCE — only its hash is stored; deliver it to the user now")
	case "list-keys":
		fs := flag.NewFlagSet("list-keys", flag.ExitOnError)
		db := dbFlag(fs)
		_ = fs.Parse(rest)
		svc := openSvc(*db)
		defer svc.Close()
		infos, err := svc.ListKeys()
		adminFatal(err)
		fmt.Printf("%-14s %-8s %5s %4s  %-10s %s\n", "PREFIX", "PLAN", "LIMIT", "USED", "EXPIRES", "REVOKED")
		for _, ki := range infos {
			rev := "-"
			if ki.Revoked {
				rev = "REVOKED"
			}
			fmt.Printf("%-14s %-8s %5d %4d  %-10s %s\n",
				ki.Prefix+"…", ki.Plan, ki.Limit, ki.Used, ki.ExpiresAt.Format("2006-01-02"), rev)
		}
	case "revoke-key":
		fs := flag.NewFlagSet("revoke-key", flag.ExitOnError)
		db := dbFlag(fs)
		_ = fs.Parse(rest)
		if fs.NArg() != 1 {
			adminUsage()
		}
		svc := openSvc(*db)
		defer svc.Close()
		adminFatal(svc.RevokeKey(fs.Arg(0)))
		fmt.Println("revoked — connected machines are kicked at the next hourly recheck")
	case "extend-key":
		fs := flag.NewFlagSet("extend-key", flag.ExitOnError)
		db := dbFlag(fs)
		months := fs.Int("months", 6, "months to add")
		_ = fs.Parse(rest)
		if fs.NArg() != 1 {
			adminUsage()
		}
		svc := openSvc(*db)
		defer svc.Close()
		adminFatal(svc.ExtendKey(fs.Arg(0), *months))
		fmt.Println("extended")
	case "unbind":
		fs := flag.NewFlagSet("unbind", flag.ExitOnError)
		db := dbFlag(fs)
		_ = fs.Parse(rest)
		if fs.NArg() != 1 {
			adminUsage()
		}
		svc := openSvc(*db)
		defer svc.Close()
		adminFatal(svc.AdminDeactivate(fs.Arg(0)))
		fmt.Println("unbound — slot freed; the machine is kicked at the next hourly recheck")
	default:
		adminUsage()
	}
}
