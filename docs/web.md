# Web channel: the web app's data contract

Roamind's web app is its second channel, beside Slack, a separate repo on Firebase. The job assistant is its first feature. Users sign in with Google, upload a resume, and give their roles, regions and notes. The worker writes the resume into the user's own memory, their Person note with `relationship: self`, which is where every application is written from; a Slack user's memory may hold it already. The worker finds postings and writes an application for each: a cover letter and an answer sheet for the form. Users can also paste links to postings they found and get an application for each. Users review them in the web app and apply on their own, with their own resume. Links sent to Roamind in Slack are drafted in the Slack thread instead, and never touch Firestore: a Slack user needn't sign in here.

The two sides share no code. They share Firestore and Cloud Storage, laid out as below. The worker uses a service account (`GOOGLE_APPLICATION_CREDENTIALS`), which bypasses the security rules; the web app is held to them.

## Identity

- The web app writes the signed-in user's Google email into their profile. The rules require it to equal `request.auth.token.email`, so a profile always carries its owner's verified email.
- The worker maps that email to Roamind's user id, `sha256(lowercase(trim(email)))`. A web user and a Slack user with the same email are the same person.
- Web-only users get `channel = 'web'` in the `users` table: they have the web app's features but not Slack's, which need a Slack identity, with a Basic Memory project set up when they first upload a resume. A Slack user who also uses the web app keeps all their Slack features.

## Storage

| Path | Written by | What |
|---|---|---|
| `resumes/{uid}/resume.pdf` | web app; deleted by the worker | The user's resume, a PDF of at most 10 MB; scans are read too. Uploading a new one replaces it. Within seconds the worker saves it in the user's workspace and has the agent write it into their memory, then deletes it here: memory holds the resume. An upload whose import failed stays until the user replaces it. |

## Firestore

### `profiles/{uid}`

Written by the web app, except `search` and `resume_import`, which only the worker writes.

| Field | Type | What |
|---|---|---|
| `email` | string | The signed-in Google email. |
| `name` | string | How applications address the user. |
| `resume_path` | string | `resumes/{uid}/resume.pdf` once uploaded. |
| `resume_generation` | string | The upload's Storage generation, written with `resume_path` after each upload; a new value is a new upload to import. |
| `roles` | string array | Roles they want, e.g. "ML Engineer". |
| `regions` | string array | Where they want to work, e.g. "Berlin", "Remote, EU". |
| `notes` | string | Anything else: seniority, industries to avoid, how they want to come across. |
| `paused` | bool | Stops the daily search. |
| `updated_at` | timestamp | When the user last saved the profile. |
| `search` | map | The latest search, for the web app to show: `status` (`running`, `done`, `skipped` or `failed`), `reason` (shown to the user), `found` (applications written) and `last_run_at`. |
| `resume_import` | map | Writing the latest upload into memory: `status` (`queued`, `running`, `done` or `failed`), `reason` (shown to the user), `generation` and `at`. An upload whose generation is here is never imported again. |

### `profiles/{uid}/applications/{id}`

Written by the worker, one per posting; `{id}` is the hash of the posting's URL. The web app may change only `status`.

| Field | Type | What |
|---|---|---|
| `title`, `company`, `location` | string | The posting's. |
| `url` | string | The posting's page. |
| `apply_url` | string | Where to apply. |
| `source` | string | `greenhouse`, `lever`, `ashby`, `workday`, `smartrecruiters`, `recruitee`, `personio`, `workable`, `oracle` or `other`. |
| `posted_at` | string | ISO date, or empty. |
| `fit` | map | `score` (0 to 10) and `reasons` (string array): why it suits the user, and what's missing. |
| `cover_letter_md` | string | The cover letter, in Markdown. |
| `answers` | array of maps | The answer sheet: `question`, `required`, `answer`, in the form's order. Answers the resume can't give (start date, salary, work authorization) read `[<name> to answer]`. |
| `status` | string | `ready` when written; the user sets `applied` or `dismissed`. |
| `created_at` | timestamp | When it was written. |
| `link_id` | string | The link the user added the posting with, when they did; else absent. |

### `profiles/{uid}/links/{id}`

A posting the user linked to. The web app creates it, with only `url`, `status: "queued"` and `created_at` (server time), and may delete it unless it is `processing`. Only the worker updates it.

| Field | Type | What |
|---|---|---|
| `url` | string | The posting's page, http(s), at most 2048 characters. |
| `status` | string | `queued` (web app), then `processing`, `done` or `failed` (worker). |
| `reason` | string | Worker: why it failed, or a note on a done one ("Already drafted."), for the user. |
| `application_id` | string | Worker: the application for the posting, once `done`. |
| `created_at` | timestamp | When the user added it. |
| `claimed_at` | timestamp | Worker: when it took the link up. |

## How the daily search uses them

- Searches run for every user at 08:00, 12:00, 16:00 and 20:00 server time. Nothing runs at sign-up: a new profile waits for the next one.
- **Reviews gate new applications.** A search runs only when the user has no open applications: every `ready` one must be marked `applied` or `dismissed` first, unless its posting has disappeared from its site, which closes it. Until then, `search.status` is `skipped`, with a reason saying how many are waiting.
- A search writes at most `ROAMIND_JOBS_PER_RUN` applications (default 5), for the postings that fit best. A posting is never shown twice.

## How links are drafted

- The worker listens for `queued` links (a collection-group query on `links`) and takes them up within seconds, 20 at a time, oldest first, marking them `processing` in a transaction. It doesn't wait for a search or for reviews, and it drafts links even while searches are paused. A link left `processing` for an hour, by a worker that stopped mid-claim, is queued again at the next search.
- In Slack, the agent submits the job links of the user's message with its `submit_job_links` tool. After the turn, the worker adds those that appear in the message to the user's profile as `queued` links, which then go the same way.
- Links to the same posting are drafted once, and all of them point at its application. A posting that already has an application isn't drafted again: its links are `done` and point at that application, even when it was reviewed. A posting that was passed over or failed before is drafted again.
- Every posting the user linked to gets an application, whatever its fit; the fit score and reasons are still shown.
- A link fails, with a `reason`, when it isn't a web address, its site is a blocked domain, the posting can't be read (closed, or behind a login), the user's memory holds no resume, or drafting fails.
- A link's application counts like any other: while it is `ready`, the daily search waits for its review.

## Security rules

For the web repo to deploy.

```
// firestore.rules
rules_version = '2';
service cloud.firestore {
  match /databases/{db}/documents {
    match /profiles/{uid} {
      allow read: if request.auth.uid == uid;
      function ownEmail() {
        return request.auth.uid == uid
          && request.auth.token.email_verified
          && request.resource.data.email == request.auth.token.email;
      }
      allow create: if ownEmail()
        && !request.resource.data.keys().hasAny(['search', 'resume_import']);
      allow update: if ownEmail()
        && !request.resource.data.diff(resource.data).affectedKeys().hasAny(['search', 'resume_import']);
      allow delete: if false;

      match /applications/{id} {
        allow read: if request.auth.uid == uid;
        allow update: if request.auth.uid == uid
          && request.resource.data.diff(resource.data).affectedKeys().hasOnly(['status'])
          && request.resource.data.status in ['ready', 'applied', 'dismissed'];
        allow create, delete: if false;
      }

      match /links/{id} {
        allow read: if request.auth.uid == uid;
        allow create: if request.auth.uid == uid
          && request.resource.data.keys().hasOnly(['url', 'status', 'created_at'])
          && request.resource.data.url is string
          && request.resource.data.url.size() <= 2048
          && request.resource.data.url.matches('https?://.+')
          && request.resource.data.status == 'queued'
          && request.resource.data.created_at == request.time;
        allow delete: if request.auth.uid == uid && resource.data.status != 'processing';
        allow update: if false;
      }
    }
  }
}
```

```
// firestore.indexes.json: the worker's listener queries links of every user by status.
{
  "indexes": [],
  "fieldOverrides": [
    {
      "collectionGroup": "links",
      "fieldPath": "status",
      "indexes": [
        { "order": "ASCENDING", "queryScope": "COLLECTION" },
        { "order": "ASCENDING", "queryScope": "COLLECTION_GROUP" }
      ]
    }
  ]
}
```

```
// storage.rules
rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /resumes/{uid}/resume.pdf {
      allow read: if request.auth.uid == uid;
      allow write: if request.auth.uid == uid
        && request.resource.size < 10 * 1024 * 1024
        && request.resource.contentType == 'application/pdf';
    }
  }
}
```

## Setting up the worker's side

1. In the Firebase console, create a service account key (Project settings → Service accounts) and save it as `data/secrets/firebase-sa.json`, readable only by you.
2. Set `FIREBASE_PROJECT_ID`, `FIREBASE_STORAGE_BUCKET` and `GOOGLE_APPLICATION_CREDENTIALS` in `.env`, and restart the service.
