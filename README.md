# sshocker: ssh + reverse sshfs + port forwarder, in Docker-like CLI

```console
$ sshocker -p 8080:80 -v .:/mnt/sshfs user@example.com
```
* Forward connections to the port 8080 on the client to the port 80 on `example.com`
* Mount the current directory on the client as `/mnt/sshfs` on `example.com`

This is akin to `docker run -p 8080:80 -v $(pwd):/mnt IMAGE`, but `sshocker` is for remote hosts, not for containers.

## Install

```console
$ go get github.com/AkihiroSuda/sshocker/cmd/sshocker
```

Tested on macOS client and Linux server. May not work on other environments, especially on Windows.

To use reverse sshfs, `sshfs` needs to be installed on the server (not on the client):

```console
$ ssh user@example.com -- sudo apt-get install -y sshfs
```

## Usage
Global flags:
* `--debug=(true|false)` (default: `false`): debug mode

### Subcommand: `run` (default)
sshocker's equivalent of `docker run`.

e.g.
```console
$ sshocker run -p 8080:80 -v .:/mnt/sshfs user@example.com
```

`run` can be ommitted, e.g.
```console
$ sshocker -p 8080:80 -v .:/mnt/sshfs user@example.com
```

Flags (similar to `docker run` flags):
* `-v LOCALDIR:REMOTEDIR[:ro]`: Mount a reverse SSHFS
* `-p [LOCALIP:]LOCALPORT:[REMOTEIP:]REMOTEPORT`: Expose a port

SSH flags:
* `-F`, `--ssh-config=FILE`: specify SSH config file used for `ssh -F`
* `--ssh-persist=(true|false)` (default: `true`): enable ControlPersit

### Subcommand: `help`
Shows help

