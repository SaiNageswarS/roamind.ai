# Web channel: the job assistant's data contract

The job assistant's web app is a separate repo on Firebase. Users sign in with Google, upload a resume, and give their roles, regions and notes. This repo's worker finds postings and writes an application for each: a tailored CV, a cover letter and an answer sheet. Users review them in the web app and apply on their own.

The two sides share no code. They share Firestore and Cloud Storage, laid out as below. The worker uses a service account (`GOOGLE_APPLICATION_CREDENTIALS`), which bypasses the security rules; the web app is held to them.

## Identity

- The web app writes the signed-in user's Google email into their profile. The rules require it to equal `request.auth.token.email`, so a profile always carries its owner's verified email.
- The worker maps that email to Roamind's user id, `sha256(lowercase(trim(email)))`. A web user and a Slack user with the same email are the same person.
- Web-only users get `channel = 'web'` in the `users` table: they have the job assistant and nothing else. A Slack user who also uses the web app keeps all their Slack features.

## Storage

| Path | Written by | What |
|---|---|---|
| `resumes/{uid}/resume.pdf` | web app | The user's resume, a PDF with a text layer (scans aren't read), at most 10 MB. Uploading a new one replaces it; the worker reads the latest the next day. |

## Firestore

### `profiles/{uid}`

Written by the web app, except `search`, which only the worker writes.

| Field | Type | What |
|---|---|---|
| `email` | string | The signed-in Google email. |
| `name` | string | How applications address the user. |
| `resume_path` | string | `resumes/{uid}/resume.pdf` once uploaded. |
| `roles` | string array | Roles they want, e.g. "ML Engineer". |
| `regions` | string array | Where they want to work, e.g. "Berlin", "Remote, EU". |
| `notes` | string | Anything else: seniority, industries to avoid, how they want to come across. |
| `paused` | bool | Stops the daily search. |
| `updated_at` | timestamp | When the user last saved the profile. |
| `search` | map | The latest search, for the web app to show: `status` (`running`, `done`, `skipped` or `failed`), `reason` (shown to the user), `found` (applications written) and `last_run_at`. |

### `profiles/{uid}/applications/{id}`

Written by the worker, one per posting; `{id}` is the hash of the posting's URL. The web app may change only `status`.

| Field | Type | What |
|---|---|---|
| `title`, `company`, `location` | string | The posting's. |
| `url` | string | The posting's page. |
| `apply_url` | string | Where to apply. |
| `source` | string | `greenhouse`, `lever`, `ashby`, `workday` or `other`. |
| `posted_at` | string | ISO date, or empty. |
| `fit` | map | `score` (0 to 10) and `reasons` (string array): why it suits the user, and what's missing. |
| `cv_md` | string | The CV tailored to the posting, in Markdown. |
| `cover_letter_md` | string | The cover letter, in Markdown. |
| `answers` | array of maps | The answer sheet: `question`, `required`, `answer`, in the form's order. Answers the resume can't give (start date, salary, work authorization) read `[<name> to answer]`. |
| `status` | string | `ready` when written; the user sets `applied` or `dismissed`. |
| `created_at` | timestamp | When it was written. |

## How the daily search uses them

- Every user gets a daily search time, between 08:00 and 22:00 server time, with users' times kept at least 90 minutes apart. Nothing runs at sign-up: a new profile waits for its time.
- **Reviews gate new applications.** A search runs only when the user has no open applications: every `ready` one must be marked `applied` or `dismissed` first, unless its posting has disappeared from its site, which closes it. Until then, `search.status` is `skipped`, with a reason saying how many are waiting.
- A search writes at most `ROAMIND_JOBS_PER_RUN` applications (default 5), for the postings that fit best. A posting is never shown twice.

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
      allow create: if ownEmail() && !('search' in request.resource.data);
      allow update: if ownEmail()
        && !request.resource.data.diff(resource.data).affectedKeys().hasAny(['search']);
      allow delete: if false;

      match /applications/{id} {
        allow read: if request.auth.uid == uid;
        allow update: if request.auth.uid == uid
          && request.resource.data.diff(resource.data).affectedKeys().hasOnly(['status'])
          && request.resource.data.status in ['ready', 'applied', 'dismissed'];
        allow create, delete: if false;
      }
    }
  }
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
