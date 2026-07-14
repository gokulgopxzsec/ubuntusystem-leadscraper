# Deploying

Everything here is manual and deliberate. Nothing auto-deploys.

## The normal case: a code change, no migration

On your laptop, before pushing:

```bash
make ci          # exactly what GitHub Actions runs. If this is red, don't push.
git push
```

Then on the server:

```bash
ssh gokul@gokul-ubunturig
cd ~/ubuntusystem-leadscraper
./scripts/deploy.sh
```

`deploy.sh` will:

1. Refuse to run if the working tree is dirty (it will not eat edits you made on the box).
2. Pull.
3. **Build to a temporary path.** A failed build leaves the running version untouched.
4. **Run the tests.** A red test suite stops the deploy; nothing is swapped.
5. Save the currently-working binary as `build/server.previous`.
6. Swap in the new binary and `systemctl restart leadscraper`.
7. Poll `/ready` for 60 seconds. That endpoint pings Postgres and Redis, so a pass
   means the app can actually serve, not just that the process launched.
8. **If it does not come up: restore the previous binary and restart.** The app is
   back on the last version that worked.

If it rolls back, the bad commit is still checked out so you can look at it:

```bash
journalctl -u leadscraper -n 50     # why did it fail?
git reset --hard HEAD~1             # go back, if you want to
./scripts/deploy.sh --force         # rebuild from the reverted code
```

## The dangerous case: a change that touches migrations/

**A rollback restores the binary. It cannot un-run SQL.** Postgres has already
committed the migration. No script can honestly promise otherwise, which is why
this one does not try.

So when `git diff --stat origin/main` shows anything under `migrations/`:

```bash
# 1. Back up FIRST. This is the only real undo you have.
docker compose exec -T postgres pg_dump -U postgres leadscraper \
  | gzip > ~/backups/leadscraper-$(date +%F-%H%M).sql.gz

# 2. Read the migration before you run it.
git diff HEAD origin/main -- migrations/

# 3. Deploy, watching.
./scripts/deploy.sh
journalctl -u leadscraper -f
```

To restore from that backup if a migration goes wrong:

```bash
sudo systemctl stop leadscraper
gunzip -c ~/backups/leadscraper-YYYY-MM-DD-HHMM.sql.gz \
  | docker compose exec -T postgres psql -U postgres -d leadscraper
sudo systemctl start leadscraper
```

## Checking on it

```bash
sudo systemctl status leadscraper
journalctl -u leadscraper -f
curl -s localhost:8080/api/v1/ready
```

## Rolling back by hand

```bash
sudo systemctl stop leadscraper
cp build/server.previous build/server
sudo systemctl start leadscraper
```
