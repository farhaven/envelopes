Envelopes
=========

This is a simple web application that helps managing your personal budget with
the [envelope system](https://en.wikipedia.org/wiki/Envelope_System).

Installation
------------
Envelopes requires `boltdb`, so you'll need to run

```
go get -u github.com/boltdb/bolt
```

before running it with `go run envelopes.go`.

Usage
-----
Point your web browser to `127.0.0.1:8081`. You can create a new envelope with
the form at the bottom. The field labelled "Balance" can be used to set an
initial balance for the envelope, the "Target" of an envelope is how much money
should be in the envelope for it to be considered "safe". I set the target for
my "Rent" envelope to my monthly rent, for example.

Above the list of envelopes, there is a field that displays the total delta of
all envelopes. This is the sum of the difference between target and balance of
all envelopes. Negative values mean that at least one envelope is below its
target value.

You can change the name, balance and target of an envelope by changing the value
in the list and pressing either the return key or the button labelled `U`. The
button labelled `X` removes an envelope. Be careful, since the funds associated
with the envelope will be lost, so you'll need to redistribute them manually.

The lowest value for the balance and target of an envelope is zero. This may
change in the future.

Backups
-------
The file `envelopes.db` contains all information from this application. Keep it
in a safe place and make regular backups!
