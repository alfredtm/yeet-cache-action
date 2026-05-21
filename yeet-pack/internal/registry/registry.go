package registry

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"golang.org/x/sync/errgroup"
)

// minimalKeychain reads ~/.docker/config.json directly without pulling in
// the docker/cli dependency that authn.DefaultKeychain uses internally.
// Drops ~2MB from the resulting binary.
type minimalKeychain struct{}

func (minimalKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return authn.Anonymous, nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".docker", "config.json"))
	if err != nil {
		return authn.Anonymous, nil
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return authn.Anonymous, nil
	}
	entry, ok := cfg.Auths[target.RegistryStr()]
	if !ok {
		return authn.Anonymous, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
	if err != nil {
		return authn.Anonymous, nil
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return authn.Anonymous, nil
	}
	return &authn.Basic{Username: parts[0], Password: parts[1]}, nil
}

func auth() remote.Option {
	return remote.WithAuthFromKeychain(minimalKeychain{})
}

// Login writes a minimal ~/.docker/config.json entry for the given registry
// so subsequent go-containerregistry calls (and any other tool that reads
// docker config) can authenticate. Replaces shelling out to crane/docker login.
func Login(registry, username, password string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".docker")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	cfgPath := filepath.Join(dir, "config.json")

	var cfg map[string]any
	if data, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	auths, _ := cfg["auths"].(map[string]any)
	if auths == nil {
		auths = map[string]any{}
	}
	authStr := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	auths[registry] = map[string]any{"auth": authStr}
	cfg["auths"] = auths

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, data, 0o600)
}

// Digest returns the sha256 digest of an image's manifest.
func Digest(ref string) (string, error) {
	r, err := name.ParseReference(ref)
	if err != nil {
		return "", err
	}
	desc, err := remote.Head(r, auth())
	if err != nil {
		return "", err
	}
	return desc.Digest.String(), nil
}

// Exists returns true if the manifest exists. Uses HEAD via remote.Head.
func Exists(ref string) (bool, error) {
	r, err := name.ParseReference(ref)
	if err != nil {
		return false, err
	}
	_, err = remote.Head(r, auth())
	if err != nil {
		if strings.Contains(err.Error(), "MANIFEST_UNKNOWN") || strings.Contains(err.Error(), "NAME_UNKNOWN") || strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// PackResult is the JSON payload returned to stdout by `yeet-pack pack`.
type PackResult struct {
	Digest string `json:"digest"`
	Tag    string `json:"tag"`
	Size   int64  `json:"size"`
}

// Pack pulls base, appends a layer containing binaryPath at pathInImage, sets
// the entrypoint, pushes to primaryTag, and applies alsoTags in parallel.
func Pack(binaryPath, pathInImage, baseRef, entrypoint, primaryTag string, alsoTags []string) (*PackResult, error) {
	baseR, err := name.ParseReference(baseRef)
	if err != nil {
		return nil, fmt.Errorf("parse base: %w", err)
	}
	base, err := remote.Image(baseR, auth())
	if err != nil {
		return nil, fmt.Errorf("pull base: %w", err)
	}

	layer, err := binaryLayer(binaryPath, pathInImage)
	if err != nil {
		return nil, fmt.Errorf("build layer: %w", err)
	}

	img, err := mutate.AppendLayers(base, layer)
	if err != nil {
		return nil, fmt.Errorf("append layer: %w", err)
	}

	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg = cfg.DeepCopy()
	cfg.Config.Entrypoint = []string{entrypoint}
	cfg.Config.Cmd = nil
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		return nil, fmt.Errorf("mutate config: %w", err)
	}

	primaryR, err := name.ParseReference(primaryTag)
	if err != nil {
		return nil, fmt.Errorf("parse primary tag: %w", err)
	}
	if err := remote.Write(primaryR, img, auth()); err != nil {
		return nil, fmt.Errorf("push primary: %w", err)
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, err
	}
	manifest, err := img.Manifest()
	if err != nil {
		return nil, err
	}
	var size int64 = manifest.Config.Size
	for _, l := range manifest.Layers {
		size += l.Size
	}

	if len(alsoTags) > 0 {
		if err := tagParallel(primaryR, alsoTags, img); err != nil {
			return nil, fmt.Errorf("retag: %w", err)
		}
	}

	return &PackResult{
		Digest: digest.String(),
		Tag:    primaryR.Name(),
		Size:   size,
	}, nil
}

// binaryLayer builds an in-memory tarball layer containing a single executable
// file at pathInImage with mode 0755. Leading slashes are stripped.
func binaryLayer(binaryPath, pathInImage string) (v1.Layer, error) {
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		return nil, err
	}
	name := strings.TrimPrefix(filepath.ToSlash(pathInImage), "/")

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0755,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Unix(0, 0),
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(data); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}

	opener := func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
	}
	return tarball.LayerFromOpener(opener, tarball.WithMediaType(types.DockerLayer))
}

func tagParallel(primary name.Reference, dsts []string, img v1.Image) error {
	repo := primary.Context()
	g, _ := errgroup.WithContext(context.Background())
	for _, t := range dsts {
		t := strings.TrimSpace(t)
		if t == "" {
			continue
		}
		g.Go(func() error {
			ref, err := resolveTag(repo, t)
			if err != nil {
				return err
			}
			return remote.Write(ref, img, auth())
		})
	}
	return g.Wait()
}

func resolveTag(repo name.Repository, t string) (name.Reference, error) {
	if strings.Contains(t, "/") || strings.Contains(t, "@") {
		return name.ParseReference(t)
	}
	return name.NewTag(repo.Name() + ":" + t)
}

// Retag points each tag in dsts at the manifest currently behind src. Runs
// dsts in parallel. Bare tags are resolved against src's repo. Uses
// remote.Tag (manifest rebind) instead of remote.Write so we skip the
// per-blob existence checks — all blobs are already in the repo.
func Retag(src string, dsts []string) ([]string, error) {
	srcRef, err := name.ParseReference(src)
	if err != nil {
		return nil, err
	}
	desc, err := remote.Get(srcRef, auth())
	if err != nil {
		return nil, fmt.Errorf("resolve src: %w", err)
	}

	applied := make([]string, len(dsts))
	g, _ := errgroup.WithContext(context.Background())
	for i, t := range dsts {
		i, t := i, strings.TrimSpace(t)
		if t == "" {
			continue
		}
		g.Go(func() error {
			ref, err := resolveTag(srcRef.Context(), t)
			if err != nil {
				return err
			}
			tag, ok := ref.(name.Tag)
			if !ok {
				return fmt.Errorf("destination must be a tag: %s", t)
			}
			if err := remote.Tag(tag, desc, auth()); err != nil {
				return err
			}
			applied[i] = ref.Name()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	out := applied[:0]
	for _, a := range applied {
		if a != "" {
			out = append(out, a)
		}
	}
	return out, nil
}
