# Privacy Notice (default v1.0)

**Operators: this is a default shipped with the HTCondor access point software.
Review it, replace every `[BRACKETED]` item, and confirm it describes what your
deployment actually does before publishing it.** It is a starting point written
to be accurate for an unmodified deployment; it is not legal advice.

This notice explains what information this access point collects when you use
its web interface or its API, why it is collected, and what is done with it.
It covers this access point only. Computing resources that run your jobs, and
any third-party service you connect to your account, are operated by others
and have their own notices.

## Who operates this service

This access point is operated by **[OPERATING ORGANIZATION]**. Questions about
this notice, or requests concerning your data, go to **[CONTACT EMAIL]**.

## What is collected

**Identity.** When you sign in, this access point receives claims from the
identity provider you authenticated with — typically a subject identifier, your
name, your email address, and any group or affiliation claims the provider
releases. It is used to identify you, to decide what you are allowed to do, and
to attribute the jobs you submit.

**Work you submit.** Submit descriptions, job attributes, the files you upload
or ask to have transferred, and the output your jobs produce are stored so that
your jobs can run and so you can retrieve their results.

**Records of jobs.** A record of each job — who submitted it, when, what
resources it requested and used, and how it ended — is retained after the job
finishes, for accounting, for troubleshooting, and for reporting aggregate usage
to the organizations that fund the resources.

**Service logs.** Requests to this service are logged, including the time, the
request path, the authenticated account, and the network address the request
came from. Logs exist to operate the service, investigate faults, and
investigate security incidents.

**Connected third-party accounts.** If you connect an external account (see
below), this access point stores the authorization that account issued it, and
a record of which files you asked it to fetch.

## Connected third-party accounts

This access point can hold an authorization that lets it retrieve files from an
account you control elsewhere — for example a cloud storage account — so those
files can be delivered to your jobs without you downloading and re-uploading
them.

Where this is offered:

- **You choose what it may reach.** The authorization is requested with the
  narrowest access the provider offers. Where the provider supports per-file
  authorization, this access point asks only for the specific files you select,
  not for your account as a whole.
- **It is used only to do what you asked.** The stored authorization is used to
  retrieve the files you selected, for delivery to the jobs you submitted. It is
  not used to browse, inventory, modify or retain anything else in that account.
- **Credentials are stored encrypted.** The authorization this access point
  holds is encrypted at rest with a key held by the service, and is never shown
  to you, to other users, or to your jobs.
- **Fetched content is transient.** File contents retrieved on your behalf are
  passed into your job's sandbox and are not retained by this access point
  afterwards, beyond whatever job output you have asked it to keep.
- **You can revoke at any time.** Disconnecting the account here discards the
  stored authorization. You may also revoke it directly with the provider, which
  takes effect immediately and independently of this service.

### Google user data

Where this access point is configured to connect Google accounts: its use and
transfer of information received from Google APIs to any other app will adhere
to the
[Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy),
including the Limited Use requirements.

Concretely, data obtained from Google is used solely to deliver the files you
selected to the jobs you submitted. It is not sold, not used for advertising,
not used to build user profiles, not used to train generalized artificial
intelligence or machine learning models, and not disclosed to any other party
except as required to run your job or as required by law.

## What is not collected

This access point does not use advertising or third-party analytics trackers,
does not sell personal information, and does not attempt to identify you beyond
the claims your identity provider releases.

## Who it is shared with

Information is shared only as needed to run the service:

- **With the resources that run your jobs.** Submitting a job sends its
  description, its input files, and the identity it runs under to the computing
  resources that execute it. Those resources are operated separately.
- **With the organizations that operate and fund this service**, in the form of
  usage records and aggregate reporting.
- **As required by law**, or in the investigation of a security incident or a
  violation of the terms of use.

It is not sold, rented, or released for marketing.

## How long it is kept

Job inputs and outputs are kept until you remove them or until the retention
period of this access point expires, whichever comes first. Job records and
service logs are kept for **[RETENTION PERIOD]**. Authorizations for connected
accounts are kept until you disconnect the account, until the provider revokes
them, or until they expire.

## Your choices

- You can disconnect any connected third-party account at any time.
- You can remove your jobs and their stored input and output.
- You can request a copy of the personal information held about you, or ask that
  it be corrected or deleted, by writing to **[CONTACT EMAIL]**. Some records
  must be retained for accounting or security purposes; where that applies, it
  will be explained in the response.

## Security

Access requires authentication, traffic is encrypted in transit, and stored
third-party credentials are encrypted at rest. No service is immune from
compromise; suspected exposure of your data should be reported to
**[CONTACT EMAIL]** immediately.

## Changes

This notice may be revised. The revision date below changes when it is, and
material changes will be announced through the service.

*Last updated: [DATE].*
