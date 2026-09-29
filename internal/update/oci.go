package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Images are published as an OCI artifact (oras push): one layer per file,
// named by the org.opencontainers.image.title annotation. The channel is
// the tag. Pulls are anonymous: a bearer token from the registry's token
// endpoint, then the manifest, then blobs, which ghcr answers with a 307 to
// a short-lived signed storage URL.

const (
	mediaOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	mediaOCIIndex       = "application/vnd.oci.image.index.v1+json"
	mediaDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	mediaDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	titleAnnotation     = "org.opencontainers.image.title"
	maxOCIManifest      = 4 << 20
)

var (
	// The distribution spec's repository name grammar.
	repoRe   = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// ValidChannel reports whether c can be a channel: an OCI tag.
func ValidChannel(c string) bool { return tagRe.MatchString(c) }

type ociDescriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Platform     *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform,omitempty"`
}

// ociManifest is either an image manifest (Layers) or an index (Manifests).
type ociManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Layers        []ociDescriptor `json:"layers"`
	Manifests     []ociDescriptor `json:"manifests"`
}

func (m *ociManifest) isIndex() bool {
	return m.MediaType == mediaOCIIndex || m.MediaType == mediaDockerList ||
		(len(m.Manifests) > 0 && len(m.Layers) == 0)
}

// pick chooses the image from an index: one for linux/amd64 or without a
// platform (an artifact), else the first.
func (m *ociManifest) pick() (ociDescriptor, error) {
	if len(m.Manifests) == 0 {
		return ociDescriptor{}, &permanentError{errors.New("image index lists no manifests")}
	}
	for _, d := range m.Manifests {
		if d.Platform == nil || (d.Platform.OS == "linux" && d.Platform.Architecture == "amd64") {
			return d, nil
		}
	}
	return m.Manifests[0], nil
}

type ociFetcher struct {
	scheme, host, repo, ref string

	tokMu sync.Mutex
	token string

	mu     sync.Mutex
	layers map[string]ociDescriptor // by title
}

func newOCI(spec, channel string) (*ociFetcher, error) {
	o := &ociFetcher{scheme: "https"}
	rest, ok := strings.CutPrefix(spec, "oci://")
	if !ok {
		rest, _ = strings.CutPrefix(spec, "oci+http://")
		o.scheme = "http"
	}
	host, path, ok := strings.Cut(rest, "/")
	if !ok || host == "" || path == "" {
		return nil, fmt.Errorf("invalid update source %q: want oci://registry/repository", spec)
	}
	ref := ""
	if repo, digest, ok := strings.Cut(path, "@"); ok {
		path, ref = repo, digest
	} else if i := strings.LastIndexByte(path, ':'); i > strings.LastIndexByte(path, '/') {
		path, ref = path[:i], path[i+1:]
	}
	if !repoRe.MatchString(path) {
		return nil, fmt.Errorf("invalid repository %q in update source", path)
	}
	if ref == "" {
		ref = channel
	}
	if ref == "" {
		ref = "main"
	}
	if !tagRe.MatchString(ref) && !digestRe.MatchString(ref) {
		return nil, fmt.Errorf("invalid channel or tag %q", ref)
	}
	o.host, o.repo, o.ref = host, path, ref
	return o, nil
}

func (o *ociFetcher) base() string { return o.scheme + "://" + o.host }
func (o *ociFetcher) remote() bool { return true }

func (o *ociFetcher) String() string {
	prefix := "oci://"
	if o.scheme == "http" {
		prefix = "oci+http://"
	}
	sep := ":"
	if digestRe.MatchString(o.ref) {
		sep = "@"
	}
	return prefix + o.host + "/" + o.repo + sep + o.ref
}

func (o *ociFetcher) getToken() string {
	o.tokMu.Lock()
	defer o.tokMu.Unlock()
	return o.token
}

// do sends a registry request with the current token. On a 401 it gets a
// token for the challenge and tries once more: tokens are short-lived, and
// a long download can outlast one.
func (o *ociFetcher) do(ctx context.Context, u string, hdr http.Header, offset int64) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		h := hdr.Clone()
		if h == nil {
			h = http.Header{}
		}
		if tok := o.getToken(); tok != "" {
			h.Set("Authorization", "Bearer "+tok)
		}
		resp, err := get(ctx, u, h, offset)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp, nil
		}
		challenge := resp.Header.Get("WWW-Authenticate")
		drain(resp)
		if err := o.authenticate(ctx, challenge); err != nil {
			return nil, err
		}
	}
}

// authenticate gets an anonymous pull token. The realm comes from the
// registry's Bearer challenge. Without one, it falls back to ghcr's
// convention, <registry>/token?scope=repository:<repo>:pull&service=<registry>.
func (o *ociFetcher) authenticate(ctx context.Context, challenge string) error {
	scope := "repository:" + o.repo + ":pull"
	realm := o.base() + "/token"
	params := url.Values{"scope": {scope}, "service": {o.host}}
	if challenge != "" {
		scheme, p := parseChallenge(challenge)
		if !strings.EqualFold(scheme, "bearer") {
			return &permanentError{fmt.Errorf("%s wants %s authentication; only anonymous pulls are supported", o.host, scheme)}
		}
		if p["realm"] != "" {
			realm = p["realm"]
			params = url.Values{"scope": {scope}}
			if p["scope"] != "" {
				params.Set("scope", p["scope"])
			}
			if p["service"] != "" {
				params.Set("service", p["service"])
			}
		}
	}
	ru, err := url.Parse(realm)
	if err != nil || (ru.Scheme != "https" && ru.Scheme != "http") || ru.Host == "" {
		return &permanentError{fmt.Errorf("%s: invalid token realm %q", o.host, realm)}
	}
	q := ru.Query()
	for k, v := range params {
		q[k] = v
	}
	ru.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := get(ctx, ru.String(), nil, 0)
	if err != nil {
		return fmt.Errorf("registry token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry token: %w", &httpError{status: resp.StatusCode, url: redact(ru.String())})
	}
	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return fmt.Errorf("registry token: %w", err)
	}
	tok := tr.Token
	if tok == "" {
		tok = tr.AccessToken
	}
	if tok == "" {
		return errors.New("registry token: empty token")
	}
	o.tokMu.Lock()
	o.token = tok
	o.tokMu.Unlock()
	return nil
}

// parseChallenge parses `Bearer realm="…",service="…",scope="…"`.
func parseChallenge(s string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(s), " ")
	params := map[string]string{}
	rest = strings.TrimSpace(rest)
	for rest != "" {
		key, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		key = strings.ToLower(strings.TrimSpace(key))
		rest = strings.TrimSpace(after)
		var val strings.Builder
		if strings.HasPrefix(rest, `"`) {
			i := 1
			for ; i < len(rest) && rest[i] != '"'; i++ {
				if rest[i] == '\\' && i+1 < len(rest) {
					i++
				}
				val.WriteByte(rest[i])
			}
			rest = rest[min(i+1, len(rest)):]
		} else {
			end := strings.IndexByte(rest, ',')
			if end < 0 {
				end = len(rest)
			}
			val.WriteString(strings.TrimSpace(rest[:end]))
			rest = rest[end:]
		}
		params[key] = val.String()
		rest = strings.TrimLeft(rest, " ,")
	}
	return scheme, params
}

// resolve fetches the manifest for the tag (through an index if need be)
// and maps layer titles to descriptors. It is cached.
func (o *ociFetcher) resolve(ctx context.Context) (map[string]ociDescriptor, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.layers != nil {
		return o.layers, nil
	}
	if o.host == "ghcr.io" && o.getToken() == "" {
		// ghcr wants a token even for public packages; ask for it up
		// front instead of after a 401.
		if err := o.authenticate(ctx, ""); err != nil {
			return nil, err
		}
	}
	m, err := o.fetchManifest(ctx, o.ref)
	if err != nil {
		return nil, err
	}
	if m.isIndex() {
		d, err := m.pick()
		if err != nil {
			return nil, err
		}
		if m, err = o.fetchManifest(ctx, d.Digest); err != nil {
			return nil, err
		}
		if m.isIndex() {
			return nil, &permanentError{fmt.Errorf("%s: nested image index", o)}
		}
	}
	layers := map[string]ociDescriptor{}
	for _, l := range m.Layers {
		title := l.Annotations[titleAnnotation]
		if title == "" {
			continue
		}
		if !digestRe.MatchString(l.Digest) {
			return nil, &permanentError{fmt.Errorf("%s: layer %s has unsupported digest %q", o, title, l.Digest)}
		}
		layers[title] = l
	}
	o.layers = layers
	return layers, nil
}

// fetchManifest GETs a manifest or index by tag or digest and checks it
// against the digest it was asked for, or the one the registry states.
func (o *ociFetcher) fetchManifest(ctx context.Context, ref string) (*ociManifest, error) {
	if !digestRe.MatchString(ref) && !tagRe.MatchString(ref) {
		return nil, &permanentError{fmt.Errorf("%s: invalid reference %q", o, ref)}
	}
	u := o.base() + "/v2/" + o.repo + "/manifests/" + ref
	hdr := http.Header{"Accept": {strings.Join([]string{mediaOCIManifest, mediaOCIIndex, mediaDockerManifest, mediaDockerList}, ", ")}}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := o.do(ctx, u, hdr, 0)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		herr := &httpError{status: resp.StatusCode, url: redact(u)}
		if resp.StatusCode == http.StatusNotFound {
			return nil, &permanentError{fmt.Errorf("%s: no such tag or manifest (%w)", o, herr)}
		}
		return nil, herr
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOCIManifest+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxOCIManifest {
		return nil, &permanentError{fmt.Errorf("%s: manifest larger than %d bytes", o, maxOCIManifest)}
	}
	sum := sha256.Sum256(body)
	got := "sha256:" + hex.EncodeToString(sum[:])
	want := resp.Header.Get("Docker-Content-Digest")
	if digestRe.MatchString(ref) {
		want = ref
	}
	if digestRe.MatchString(want) && want != got {
		return nil, fmt.Errorf("%s: manifest digest %s, want %s", o, got, want)
	}
	var m ociManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, &permanentError{fmt.Errorf("%s: manifest: %w", o, err)}
	}
	if m.MediaType == "" {
		m.MediaType, _, _ = mime.ParseMediaType(resp.Header.Get("Content-Type"))
	}
	return &m, nil
}

func (o *ociFetcher) digest(ctx context.Context, name string) (string, int64, error) {
	layers, err := o.resolve(ctx)
	if err != nil {
		return "", 0, err
	}
	l, ok := layers[name]
	if !ok {
		return "", 0, &permanentError{fmt.Errorf("%s has no layer titled %q", o, name)}
	}
	return l.Digest, l.Size, nil
}

// open GETs a blob. Every (re)open asks the registry again, because the
// signed storage URL from an earlier redirect may have expired.
func (o *ociFetcher) open(ctx context.Context, name string, offset int64) (io.ReadCloser, error) {
	d, _, err := o.digest(ctx, name)
	if err != nil {
		return nil, err
	}
	resp, err := o.do(ctx, o.base()+"/v2/"+o.repo+"/blobs/"+d, nil, offset)
	if err != nil {
		return nil, err
	}
	return finish(ctx, resp, offset)
}
