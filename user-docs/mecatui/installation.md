---
sidebar_position: 2.5
title: Install mecatui
description:
  Install mecatui with Homebrew, Conda-forge, a verified release archive, or a
  source build.
---

import ReleaseArchivesAndSource from '../_partials/release-archives-and-source.mdx';

# Install mecatui

Install `mecatui`, the terminal client, and `mecated`, the standalone server,
on macOS or Linux.

## Install with Homebrew

```sh
brew install stacklok/tap/mecatl
mecatui --version
```

## Install with Conda-forge

If you manage your environment with Conda, install the
[Mecatl package on Conda-forge](https://anaconda.org/conda-forge/mecatl) with
your preferred package manager:

|Package manager|Command|
|-|-|
|Conda|`conda install -c conda-forge mecatl`|
|Mamba|`mamba install -c conda-forge mecatl`|
|Pixi|`pixi add mecatl`|

<ReleaseArchivesAndSource />

## Next steps

- [Run your first local session](./getting-started.md) to use `mecatui` in a
  trusted project.
- [Connect to a server](./remote-servers.md) to use a remote Mecatl service.
