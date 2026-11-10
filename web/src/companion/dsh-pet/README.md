# Vendored dsh-pet pet card, animation and motion

Copied from [PC2005-cloud/dsh-pet](https://github.com/PC2005-cloud/dsh-pet) commit `10f6c868b8110c897af33d6137a3d85e84b665ba` by `scripts/vendor-dsh-pet.py`. The code is MIT, see `LICENSE`, which the Web UI's build ships as `assets/third-party-licenses.txt` (`make web-build`). Do not edit the copied files by hand: add a patch to `../dsh-pet-patches/` or change the script, and vendor again. This README is written by the script too, and `files.sha256` lists every file it wrote with its hash, which `make vendor-check` checks without a dsh-pet checkout.

The animations this code plays are not in Git and are not MIT. The character is the community-made DeepSeek 大肥鱼, not an official DeepSeek character: the original 溟月 is by [@上善无形](https://space.bilibili.com/4456176) and the maid design by [@ZipZipPipe](https://space.bilibili.com/4168597), licensed [CC BY-NC-SA 4.0](https://creativecommons.org/licenses/by-nc-sa/4.0/) with no commercial use in any form; dsh-pet's animations, prompts and source videos may not be used commercially. Anything showing the character credits <https://github.com/PC2005-cloud/dsh-pet>, and A-NAS must replace it before any commercial release.

Patches applied: `0002-work-status-bridge-without-the-harness-setup.patch`, `0003-poll-state-in-the-page.patch`, `0004-card-without-the-balance.patch`, `0005-card-without-voice.patch`, `0006-card-runtime-without-the-menu-todos-settings-and-chat.patch`, `0007-a-nas-routes-and-animation-addresses.patch`, `0008-styles-for-a-nas.patch`, `0009-no-click-sound.patch`, `0010-no-score-popup.patch`, `0011-rebuild-the-animation-chain-after-a-strictmode-remount.patch`, `0012-answer-the-card-actions-in-the-page.patch`, `0013-freeze-the-card-while-the-page-is-paused.patch`, `0014-tell-the-page-where-the-pet-was-left.patch`, `0015-a-stage-without-its-animation-plays-nothing.patch`, `0016-a-switch-that-loads-during-a-pause-waits-for-its-end.patch`, `0017-no-motion-sensors-while-tilt-and-shake-are-off.patch`, `0018-no-request-when-the-page-does-not-answer-an-action.patch`, `0019-the-keyboard-presses-the-pet.patch`, `0020-keys-for-the-card-view-children.patch`, `0021-the-host-decisions-know-the-page-s-one-pet.patch`, `0022-the-host-plays-no-animation-from-an-address.patch`, `0023-the-bubble-is-text-only.patch`, `0024-trace-lines-only-in-a-development-build.patch`, `0025-what-vnas-says-shows-over-the-turn-s-end.patch`, `0026-a-turn-s-stage-leaves-a-move-vnas-asked-for-alone.patch`, `0027-a-switch-waits-for-its-own-clip.patch`, `0028-a-stage-plays-the-clip-its-step-asks-for.patch`, `0029-a-turn-s-end-stops-every-step-clip-and-a-load-holds-the-front-s-end.patch`, `0030-a-command-s-answer-follows-the-switch-that-plays-its-clip.patch`, `0031-a-clip-that-does-not-play-moves-the-chain-on.patch`, `0032-a-clip-vnas-asked-for-plays-to-its-end-before-the-stage-s.patch`, `0033-no-fetchstate.patch`, `0034-a-command-s-taken-playback-id-does-not-switch-again.patch`, `0035-an-earlier-clip-s-error-is-not-the-switch-s.patch`, `0036-a-clip-that-did-not-play-waits-for-a-freeze-to-end.patch`, `0037-a-held-back-stage-goes-when-the-card-stops-following-the-turn.patch`.

`animations.sha256` lists the 223 animation files of this commit and of `https://github.com/PC2005-cloud/dsh-pet/releases/download/assets-mov/dsh-pet-mov-assets-mov.zip` (SHA-256 `654e42f14b647a1e59a4c6f05694273dcf23edb063a5e49b14fc05a1fad3b467`); installed, they are companion animation pool `efc3b4c6eebf8963`, the pool `scripts/install-media-pool.sh` pins. Upstream has since replaced that release asset: the pinned zip comes from a kept copy, and the script refuses any other.

## No updates

1. A-NAS does not follow upstream. A new dsh-pet release or commit sets nothing off.
2. A device never updates it. The code is compiled into the Web desktop that anas-api serves; the animations are a pool an administrator installs with `scripts/install-media-pool.sh`; the NAS fetches nothing of dsh-pet from the network.
3. Upgrading is a deliberate dependency upgrade, only when upstream fixes a security problem or a bug A-NAS needs in the copied files, when a new character or new animations are wanted, or when the companion's next version is planned.
4. Dependabot does not see vendored code, so no security alert comes on its own. Before each milestone, see whether upstream changed what is copied (an empty output means no):

   ```bash
   git -C <dsh-pet checkout> fetch origin HEAD && git -C <dsh-pet checkout> diff --stat 10f6c868b811..FETCH_HEAD -- \
     dsh-pet/src/client dsh-pet/src/host dsh-pet/src/shared \
     dsh-pet/assets/config.jsonc dsh-pet/assets/webm LICENSE
   ```

## Upgrading

1. Set `PINNED` in `scripts/vendor-dsh-pet.py` to the new commit, and `MOV_ZIP` when the HEVC animations changed too; keep a copy of that zip, since upstream may replace it.
2. Check the commit out in a clean dsh-pet checkout and run the script; adapt the patches in `../dsh-pet-patches/` one by one until they apply and the script passes.
3. The new `animations.sha256` is a new pool: set `companion_pool` in `scripts/install-media-pool.sh` to the pool the script prints, and update the install runbook.
4. Run `make vendor-check DSH_PET=<checkout> DSH_PET_MOV_ZIP=<zip>` and `make check`, then try the companion in a browser, Safari included.
5. Stage and install the new pool on the NAS (`scripts/deploy-media-pool.ps1 -Kind companion`, then `install-media-pool.sh companion`), and have the upgrade reviewed as a dependency upgrade.
