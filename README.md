# ackctl

Command-line tools for [AWS Controllers for Kubernetes][ack] (ACK).

[ack]: https://github.com/aws-controllers-k8s/community

> **Status: alpha.** Flags and output may change between releases. Install with
> `go install`; there are no prebuilt binaries yet.

## Commands

| Command | Purpose |
|---------|---------|
| `ackctl adopt` | Bring existing AWS resources under ACK management, discovered by tag |
| `ackctl list adoptable` | Show which resource kinds can be adopted by tag, and why the rest cannot |

Run `ackctl <command> --help` for flags and examples.

## Install

Needs Go 1.24 or newer.

```bash
go install github.com/aws-controllers-k8s/ackctl/cmd/ackctl@latest
```

That installs the `ackctl` binary into `$(go env GOPATH)/bin`, which must be on your `PATH`.
Pin a specific release instead of `@latest` with `...cmd/ackctl@v0.0.1`.

Verify, and check what you installed:

```bash
ackctl version
```

From a clone:

```bash
make build      # -> bin/ackctl
make install    # -> $GOPATH/bin/ackctl
```

## Example

Adopt every EKS Nodegroup tagged `Environment=prod`:

```bash
ackctl adopt --service eks --kind Nodegroup --tag Environment=prod | kubectl create -f -
```

Manifests go to stdout; nothing is applied to a cluster for you. The default flags emit
observe-only CRs — read-only, and retained on delete — so ACK reconciles the resource
without modifying it. Pass `--read-only=false --deletion-policy delete` to let ACK own it.

## Documentation

<!-- TODO: link the ACK website page covering the CLI and its commands, once published. -->
A full command reference is being written for the ACK website. Until then, `--help`
is authoritative, and the [design proposal][proposal] covers the adoption mapping and
the reasoning behind it.

[proposal]: https://github.com/aws-controllers-k8s/community/blob/main/docs/design/proposals/ackctl/adopt-by-tags.md

## Development

```bash
make test               # lint, unit tests, race detector
make test-integration   # talks to real AWS; needs credentials and AWS_REGION
make catalog            # regenerate the embedded adoption catalog
make verify-catalog     # fail if the committed catalog is stale
```

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
