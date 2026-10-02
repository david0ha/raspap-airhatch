# Security policy

## Reporting a vulnerability

Report vulnerabilities in this layer, airhatchd or the plugin privately
through GitHub's private vulnerability reporting:
<https://github.com/david0ha/raspap-airhatch/security/advisories/new>.
Do not open a public issue for a vulnerability.

Include the affected revision, the image configuration with all credentials
removed, the impact and the steps to reproduce. Do not attach `site.conf`
files, the first-boot credentials notice, wireless passwords or complete
device images. Please allow time for a fix before disclosing details
publicly.

Only the current recipe set on the default branch is maintained. A
successful image build is not a claim that the add-on, RaspAP or their
dependencies are free of vulnerabilities; rebuild images when Yocto, RaspAP
or this layer publish fixes.

## What belongs elsewhere

Report defects in unchanged RaspAP code through RaspAP's upstream security
channel (<https://github.com/RaspAP/raspap-webgui>), and defects in
meta-raspap's integration to
<https://github.com/david0ha/meta-raspap/security/advisories/new>.

## Security notes

First boot replaces RaspAP's public defaults (admin password `secret`,
hotspot passphrase `ChangeMe`) with per-device values and writes them to
`/boot/raspap-airhatch.txt`. Note them, change them in RaspAP and delete that
file. Anyone who knows the hotspot passphrase can reach RaspAP's web
interface while the fallback hotspot is open; RaspAP grants www-data powerful
sudo operations, so keep the passphrase and the admin password private. The
layer README's "Security model" section describes the add-on's measures and
the upstream issues it inherits.
