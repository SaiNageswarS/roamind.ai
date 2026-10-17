Read {{.UserName}}'s resume below and list what it says about them, for
their profile. Today is {{.Date}}.

<resume>
{{.Resume}}
</resume>

Give each fact once, as one line, in these formats, keeping the resume's
own wording and numbers:

- contact: an email, phone number or profile link (LinkedIn, GitHub, a
  website), one per line.
- location: where they live, e.g. "Frankfurt am Main, Germany".
- experience: one role per line, newest first: "<role>, <organization>,
  <YYYY-MM> to <YYYY-MM or present>: <highlights>", with the role's main
  achievements as the highlights, separated by semicolons. Use the month
  the resume gives, else -01.
- education: one degree per line: "<qualification>, <institution>, <YYYY>
  to <YYYY>".
- skill: skills and tools, a few lines grouped as the resume groups them,
  e.g. "Languages: Go, Python, Java".
- project: one per line: "<name>: <what it is and what came of it>".
- publication: one per line: "<title>, <venue> <YYYY>".

Leave a list empty when the resume has nothing for it. Take only what the
resume says; don't add or infer facts.
