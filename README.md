# sshocker: ssh + reverse sshfs + port forwarder, in Docker-like CLI

```console
$ sshocker -p 8080:80 -v .:/mnt/sshfs user@example.com
```
* Forward connections to the port 8080 on the client to the port 80 on `example.com`
* Mount the current directory on the client as `/mnt/sshfs` on `example.com`

This is akin to `docker run -p 8080:80 -v $(pwd):/mnt IMAGE`, but `sshocker` is for remote hosts, not for containers.

## Install

Download from https://github.com/lima-vm/sshocker/releases .

To download using curl:
```
curl -o sshocker --fail -L https://github.com/lima-vm/sshocker/releases/latest/download/sshocker-$(uname -s)-$(uname -m)
chmod +x sshocker
```

To compile from source:
```console
make
sudo make install
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

`run` can be omitted, e.g.
```console
$ sshocker -p 8080:80 -v .:/mnt/sshfs user@example.com
```

Flags (similar to `docker run` flags):
* `-v LOCALDIR:REMOTEDIR[:ro]`: Mount a reverse SSHFS
* `-p [[LOCALIP:]LOCALPORT:]REMOTEPORT`: Expose a port

SSH flags:
* `-F`, `--ssh-config=FILE`: specify SSH config file used for `ssh -F`
* `--ssh-persist=(true|false)` (default: `true`): enable ControlPersist

SSHFS flags:
* `--sshfs-noempty` (default: `false`): enable sshfs nonempty

SFTP server flags:
* `--driver=DRIVER` (default: `auto`): SFTP server driver. `builtin` (legacy) or `openssh-sftp-server` (robust and secure, recommended).
   `openssh-sftp-server` is chosen by default when the OpenSSH SFTP Server binary is detected.
* `--openssh-sftp-server=BINARY`: OpenSSH SFTP Server binary.
   Automatically detected when installed in well-known locations such as `/usr/libexec/sftp-server`.

### Subcommand: `help`
Shows help

