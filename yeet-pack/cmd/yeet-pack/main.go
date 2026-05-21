package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alfredtm/yeet-cache-action/yeet-pack/internal/hash"
	"github.com/alfredtm/yeet-cache-action/yeet-pack/internal/registry"
)

const usage = `yeet-pack - fast in-memory OCI image packing

usage: yeet-pack <command> [flags]

commands:
  hash    compute 12-char source content address
  check   check whether an image tag exists
  pack    build an OCI image in memory and push it
  tag     apply additional tags to an existing image in parallel
  login   write registry credentials to ~/.docker/config.json
  digest  print the sha256 digest of an image's manifest
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hash":
		cmdHash(os.Args[2:])
	case "check":
		cmdCheck(os.Args[2:])
	case "pack":
		cmdPack(os.Args[2:])
	case "tag":
		cmdTag(os.Args[2:])
	case "login":
		cmdLogin(os.Args[2:])
	case "digest":
		cmdDigest(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cmdHash(args []string) {
	fs := flag.NewFlagSet("hash", flag.ExitOnError)
	paths := fs.String("paths", "", "comma-separated paths under HEAD to hash")
	extra := fs.String("extra", "", "arbitrary string mixed into the hash")
	fs.Parse(args)
	if *paths == "" {
		fmt.Fprintln(os.Stderr, "hash: --paths required")
		os.Exit(2)
	}
	h, err := hash.Compute(splitCSV(*paths), *extra)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash:", err)
		os.Exit(1)
	}
	fmt.Println(h)
}

func cmdCheck(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	image := fs.String("image", "", "full image reference including tag")
	fs.Parse(args)
	if *image == "" {
		fmt.Fprintln(os.Stderr, "check: --image required")
		os.Exit(2)
	}
	ok, err := registry.Exists(*image)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check:", err)
		os.Exit(2)
	}
	if ok {
		fmt.Println("hit")
		os.Exit(0)
	}
	fmt.Println("miss")
	os.Exit(1)
}

func cmdPack(args []string) {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	binary := fs.String("binary", "", "path to the built binary")
	pathInImage := fs.String("binary-path-in-image", "/app/server", "where the binary lives inside the image")
	base := fs.String("base", "", "base image reference")
	entrypoint := fs.String("entrypoint", "", "entrypoint (defaults to --binary-path-in-image)")
	tag := fs.String("tag", "", "primary tag to push to")
	alsoTag := fs.String("also-tag", "", "comma-separated additional tags")
	fs.Parse(args)

	for k, v := range map[string]string{"binary": *binary, "base": *base, "tag": *tag} {
		if v == "" {
			fmt.Fprintf(os.Stderr, "pack: --%s required\n", k)
			os.Exit(2)
		}
	}
	ep := *entrypoint
	if ep == "" {
		ep = *pathInImage
	}
	res, err := registry.Pack(*binary, *pathInImage, *base, ep, *tag, splitCSV(*alsoTag))
	if err != nil {
		fmt.Fprintln(os.Stderr, "pack:", err)
		os.Exit(1)
	}
	// Side channel: when running under GitHub Actions, append the digest to
	// $GITHUB_ENV so the action's post hook can read it without making
	// another registry call.
	if envFile := os.Getenv("GITHUB_ENV"); envFile != "" {
		f, err := os.OpenFile(envFile, os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "YEET_PACK_DIGEST=%s\n", res.Digest)
			f.Close()
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(res); err != nil {
		fmt.Fprintln(os.Stderr, "pack:", err)
		os.Exit(1)
	}
}

func cmdLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	registryName := fs.String("registry", "", "registry hostname (e.g. ghcr.io)")
	username := fs.String("username", "", "username")
	password := fs.String("password", "", "password (or pass via --password-stdin)")
	stdin := fs.Bool("password-stdin", false, "read password from stdin")
	fs.Parse(args)
	if *registryName == "" || *username == "" {
		fmt.Fprintln(os.Stderr, "login: --registry and --username required")
		os.Exit(2)
	}
	pw := *password
	if *stdin {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "login:", err)
			os.Exit(1)
		}
		pw = strings.TrimRight(string(b), "\r\n")
	}
	if pw == "" {
		fmt.Fprintln(os.Stderr, "login: empty password")
		os.Exit(2)
	}
	if err := registry.Login(*registryName, *username, pw); err != nil {
		fmt.Fprintln(os.Stderr, "login:", err)
		os.Exit(1)
	}
}

func cmdDigest(args []string) {
	fs := flag.NewFlagSet("digest", flag.ExitOnError)
	image := fs.String("image", "", "full image reference including tag")
	fs.Parse(args)
	if *image == "" {
		fmt.Fprintln(os.Stderr, "digest: --image required")
		os.Exit(2)
	}
	d, err := registry.Digest(*image)
	if err != nil {
		fmt.Fprintln(os.Stderr, "digest:", err)
		os.Exit(1)
	}
	fmt.Println(d)
}

func cmdTag(args []string) {
	fs := flag.NewFlagSet("tag", flag.ExitOnError)
	src := fs.String("src", "", "source image tag")
	tags := fs.String("tags", "", "comma-separated destination tags")
	fs.Parse(args)
	if *src == "" || *tags == "" {
		fmt.Fprintln(os.Stderr, "tag: --src and --tags required")
		os.Exit(2)
	}
	applied, err := registry.Retag(*src, splitCSV(*tags))
	if err != nil {
		fmt.Fprintln(os.Stderr, "tag:", err)
		os.Exit(1)
	}
	for _, a := range applied {
		fmt.Println(a)
	}
}
