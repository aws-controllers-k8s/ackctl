# ack

Command-line tools for [AWS Controllers for Kubernetes][ack] (ACK).

[ack]: https://github.com/aws-controllers-k8s/community

> **Status: not released.** There are no published binaries yet — build from source
> as below.

## Commands

| Command | Purpose |
|---------|---------|
| `ack adopt` | Bring existing AWS resources under ACK management, discovered by tag |
| `ack list adoptable` | Show which resource kinds can be adopted by tag, and why the rest cannot |

Run `ack <command> --help` for flags and examples.

## Install

```bash
make build      # -> bin/ack
make install    # -> $GOPATH/bin/ack
```

`ack --version` reports the tag and commit it was built from.

## Example

Adopt every EKS Nodegroup tagged `Environment=prod`:

```bash
ack adopt --service eks --kind Nodegroup --tag Environment=prod | kubectl create -f -
```

Manifests go to stdout; nothing is applied to a cluster for you. The first run is
observe-only — emitted resources are read-only and retained on delete, so handing
management to ACK is a second, deliberate step.

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
```

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
