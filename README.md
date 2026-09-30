# Notes

A small note-taking app: a page where you read and add notes, a JSON API, and a health check. It's the app for the [ComputeSphere Learn](https://learn.computesphere.com) challenge *Ship it*: fork this repository and have ComputeSphere build and run it from your fork.

Go, standard library only. The `Dockerfile` in this folder builds it.

## What it needs to run

| | Value |
| --- | --- |
| Port | `8080` |
| Health check path | `/healthz` |
| Volume mount path | `/data` |
| Variable | `NOTES_TITLE`: the heading on the page, for example `Team notes` |
| Secret | `NOTES_WRITE_KEY`: the key anyone adding a note must give. At least 16 characters, for example `demo-not-a-real-key-0000` |

- **Store `NOTES_WRITE_KEY` as a secret**, not a plain variable. Choose your own value; the one above is only an example.
- **Mount a volume at `/data`.** Notes are kept in `/data/notes.json`. Without a volume there, they're lost on the next redeploy.
- **Run one copy.** A volume attaches to one spherelet at a time, so keep the service at 1 spherelet.

Until both settings are set, Notes still starts and shows its page, but `/healthz` returns `503` with the names of what's missing, and nobody can add a note. Settings are read when the app starts, so redeploy after changing them.

The page tells you where your notes are: if it says they're on the spherelet's own disk, the volume isn't mounted at `/data`.

## Using it

Open the service's URL, write a note, enter the write key and choose **Add note**.

From a terminal:

```bash
curl -X POST https://<your URL>/api/notes \
  -H "Authorization: Bearer $NOTES_WRITE_KEY" \
  -H "Content-Type: application/json" \
  -d '{"text": "Shipped it"}'

curl https://<your URL>/api/notes
```

| Request | Response |
| --- | --- |
| `GET /` | The page: your notes, newest first, and a form to add one |
| `GET /api/notes` | `{"notes": [...]}`, newest first |
| `POST /api/notes` | Adds `{"text": "..."}` (1 to 1000 characters) with `Authorization: Bearer <write key>`; `201` and the note. `403` if the key is wrong |
| `GET /healthz` | `200` and `{"status":"ok"}` when configured; `503` and what's missing when not |

It keeps the latest 200 notes. Logs are JSON, one line per request; the write key is never logged or shown.

## Run it on your machine

With [Docker](https://docs.docker.com/get-docker/):

```bash
docker build -t notes .
mkdir -p notes-data
docker run --rm -p 8080:8080 \
  -e NOTES_TITLE="Team notes" \
  -e NOTES_WRITE_KEY="demo-not-a-real-key-0000" \
  -v "$PWD/notes-data:/data" \
  notes
```

Then open <http://localhost:8080>. Or, with Go 1.23 or later: `DATA_DIR=./notes-data NOTES_TITLE="Team notes" NOTES_WRITE_KEY="demo-not-a-real-key-0000" go run .` (`DATA_DIR` changes the folder from `/data`).

## License

MIT
