# Step 3: Your identity and contact details

## What this step does

It records your name, email, phone and address. Application forms are filled
from these values, and typed signatures use your full name. Without them the
forms cannot be filled.

## Before you start

Have these ready, all of them your own: full legal name, a working email,
phone number with country code, and your address. Street address and postal
code may be left to yourself by writing `@ask-me` (in the dashboard, tick Ask
me at each application under the field): the form field is then left blank for
you to fill by hand. Nothing here is guessed or invented; if you do
not have a value, leave the field out.

In the dashboard, once your CV is saved (step 2), this step offers the name,
email, phone, place and links it finds there for the fields still empty. Use
each one or all of them; nothing is stored until you press Save identity. A
place written "City, ST" with a US state also offers the country US (not for
the codes that also name a country: AR, DE, ID, IL, IN, or Georgia). The
interactive `applysmith setup` offers the same values as the default answer of
each empty question (press Enter to take one); a value you already stored
stays the default.

The seeded workspace holds a fictional persona ("Alex Example"). The step does
not count example values, a reserved example email domain or a fictional phone
number as yours.

## Steps

See which fields the step accepts:

```
applysmith setup schema identity
```

Write a JSON file with your values, for example `identity.json`:

```json
{
  "full_name": "<your full legal name>",
  "email": "<your email>",
  "phone": "<your phone, + and digits>",
  "city": "<your city>",
  "region": "<your state or province>",
  "country": "<two-letter country code>",
  "street_address": "@ask-me",
  "postal_code": "@ask-me"
}
```

Check it without saving anything, then save it:

```
applysmith setup apply identity --file identity.json --dry-run
applysmith setup apply identity --file identity.json
```

A field you leave out keeps its stored value. Optional fields are
`preferred_name`, `county`, `date_of_birth` (YYYY-MM-DD, used only to answer
age questions), `linkedin`, `github`, `portfolio` and `other_links`.

If the saved values are still the shipped example persona and you really are
that persona (for example in a trial), confirm it:

```
applysmith setup ack identity
```

## Where this is stored

In `profile.yml` in your workspace, under `candidate`. Nothing about identity
goes to the keychain. Delete the file's contents and the step reads as undone
again.

## How to check it worked

```
applysmith doctor --only identity
```

```text
[PASS] identity.fields   identity is complete
```

`applysmith setup status` shows step 3 as `complete`.

## If something goes wrong

| What you see | What to do |
|---|---|
| `input rejected` with `email: is on a reserved example domain` | Enter your own address; example domains are refused on purpose. |
| `phone` rejected | Use international form: a `+`, then digits. A US, Canadian or UK national number is converted. |
| `country` rejected | Use the two-letter ISO code, for example `GB` or `US`. |
| A field says `empty` after saving | The key name in the message (for example `candidate.address.city`) is the place in `profile.yml`; run `applysmith setup next` to see what is left. |

Next: [Step 4: work authorization](04-work-authorization.md).
