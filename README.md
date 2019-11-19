# gh - The GitHub CLI tool

The #ce-cli team is working on a publicly available CLI tool to reduce the friction between GitHub and one's local machine for people who use the command line primarily to interact with Git and GitHub. https://github.com/github/releases/issues/659

This tool is an endeavor separate from [github/hub](https://github.com/github/hub), which acts as a proxy to `git`, since our aim is to reimagine from scratch the kind of command line interface to GitHub that would serve our users' interests best.

# Installation

_warning, gh is in a very alpha phase_

`brew install github/gh/gh`

That's it. You are now ready to use `gh` on the command line. 🥳

# Process

- [Demo planning doc](https://docs.google.com/document/d/18ym-_xjFTSXe0-xzgaBn13Su7MEhWfLE5qSNPJV4M0A/edit)
- [Weekly tracking issue](https://github.com/github/gh-cli/labels/tracking%20issue)
- [Weekly sync notes](https://docs.google.com/document/d/1eUo9nIzXbC1DG26Y3dk9hOceLua2yFlwlvFPZ82MwHg/edit)

# How to create a release

This can all be done from your local terminal.

1. `git tag 'vVERSION_NUMBER' # example git tag 'v0.0.1'`
2. `git push origin vVERSION_NUMBER`
3. Wait a few minutes for the build to run and CI to pass. Look at the [actions tab](https://github.com/github/gh-cli/actions) to check the progress.
4. Go to https://github.com/github/homebrew-gh/releases and look at the release
