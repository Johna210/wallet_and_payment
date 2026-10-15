# Wallet & Payment

A service that holds users' money in wallets and moves it in, out, and between them while keeping the financial state consistent and auditable.

## Holding money

**User**:
The external owner of wallets, known only by an opaque identifier; this service does not manage users.
_Avoid_: Customer, account holder

**Wallet**:
A store of value owned by one User in exactly one Currency, with a Balance and a Wallet Status. A User has at most one Wallet per Currency.
_Avoid_: Account, purse

**Balance**:
The amount of money currently held in a Wallet; never negative, and always equal to the sum of the Wallet's Ledger Entries.
_Avoid_: Funds, available amount

**Currency**:
An ISO-4217 code from the supported set, fixing how many minor units make one major unit.

**Wallet Status**:
The lifecycle state of a Wallet: Active, Frozen, or Closed.

**Active**:
A Wallet that may send and receive money.

**Frozen**:
A Wallet temporarily barred from sending or receiving money; may return to Active.
_Avoid_: Suspended, locked, blocked

**Closed**:
A terminal Wallet Status, reachable only at zero Balance; a Closed Wallet never moves money again.
_Avoid_: Deleted, deactivated

## Moving money

**Transaction**:
The record of one completed financial operation (a Deposit, Withdrawal, or Transfer) and the only evidence that money moved.
_Avoid_: Payment, operation, transfer (as a generic term)

**Deposit**:
A Transaction that brings money from outside the system into a Wallet.
_Avoid_: Top-up, credit, fund

**Withdrawal**:
A Transaction that takes money out of a Wallet to outside the system.
_Avoid_: Payout, debit, cash-out

**Transfer**:
A Transaction that moves money from one Wallet to another Wallet of the same Currency, creating and destroying none.
_Avoid_: Payment, send

**Ledger Entry**:
A signed change to one Wallet's Balance produced by a Transaction; a Transfer produces two that sum to zero.
_Avoid_: Posting, line item

**Rejection**:
An attempted financial operation refused by a business rule (e.g. insufficient Balance, Frozen Wallet); it is not a Transaction.
_Avoid_: Failed transaction

**Idempotency Key**:
A client-chosen identifier that makes retrying a money-moving request produce the original Transaction rather than a new one.
