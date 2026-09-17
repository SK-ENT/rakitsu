# Contributing to Rakitsu

Thanks for your interest in contributing to **Rakitsu**.

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). By participating you agree to uphold it.

## License

Rakitsu is licensed under the [Business Source License 1.1](LICENSE). By contributing, you agree that your contributions will be licensed under the same terms. The license converts to Apache License, Version 2.0 four years after each version's first public release.

Please read the `LICENSE` and [`LICENSING.md`](LICENSING.md) files before contributing so you understand the terms and the reasoning behind them.

## Developer Certificate of Origin (DCO)

All contributions must include a `Signed-off-by` line in the commit message, certifying that you wrote the code (or have the right to submit it) and agree to license it under the same terms as the rest of the project (see [License](#license) above). There's no separate Contributor License Agreement — you keep the copyright to your contribution, and it's licensed to the project under the BSL terms, same as everything else here.

```
Signed-off-by: Your Name <your.email@example.com>
```

Add it automatically with `git commit -s`.

This follows the [Developer Certificate of Origin](https://developercertificate.org/):

> By making a contribution to this project, I certify that:
>
> (a) The contribution was created in whole or in part by me and I have the right to submit it under the open source license indicated in the file; or
>
> (b) The contribution is based upon previous work that, to the best of my knowledge, is covered under an appropriate open source license and I have the right under that license to submit that work with modifications; or
>
> (c) The contribution was provided directly to me by some other person who certified (a), (b) or (c) and I have not modified it.
>
> (d) I understand and agree that this project and the contribution are public and that a record of the contribution is maintained indefinitely.

## How to Contribute

1. Fork the repository
2. Create a feature branch: `git checkout -b feat/your-feature`
3. Make your changes
4. Ensure tests pass: `make test`
5. Ensure the build works: `make build-embedded`
6. Commit with DCO sign-off: `git commit -s -m "feat: your feature"`
7. Push and open a pull request

## Development Setup

```bash
# Requirements: Go 1.25+, Node.js 20+
git clone https://github.com/SK-ENT/rakitsu.git
cd rakitsu
make build-embedded    # frontend + Go binary
make test              # run tests
make lint              # run linters
```

## Conventions

- **Commits:** [Conventional Commits](https://www.conventionalcommits.org/) — `feat:`, `fix:`, `chore:`, `refactor:`, `test:`, `docs:`
- **Go:** Interfaces in dedicated files, implementations in sub-packages, `NewX(config) *X` constructors
- **Vue:** `<script setup lang="ts">`, composables with `use` prefix, scoped CSS
- **Branches:** `feat/<name>`, `fix/<name>` — never commit directly to main

## Reporting Issues

Open an issue on [GitHub Issues](https://github.com/SK-ENT/rakitsu/issues) with:
- What you expected
- What happened
- Steps to reproduce
- Rakitsu version (`rakitsu --version`)
