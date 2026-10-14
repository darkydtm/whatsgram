package admin

func (p Policy) DecideAccountAction(in Input) Decision {
	if got := p.telegramAction(in); got.Kind != Bridge {
		return got
	}
	if !p.CanDriveAccount(in.UserID) {
		return Decision{Kind: Reject, Code: "not-account-driver", Reason: "Only the designated numeric driver may react or vote as the linked account."}
	}
	return Decision{Kind: Bridge, Code: "account-driver", Reason: "Authorized linked-account intent."}
}

func (p Policy) DecideMutation(in Input, originUserID int64, explicitOverride bool) Decision {
	if got := p.telegramAction(in); got.Kind != Bridge {
		return got
	}
	if !p.CanMutateOriginal(in.UserID, originUserID, explicitOverride) {
		return Decision{Kind: Reject, Code: "not-original-owner", Reason: "Mutation requires the original author or an explicit audited override."}
	}
	return Decision{Kind: Bridge, Code: "original-mutation", Reason: "Authorized original-message intent."}
}

func (p Policy) telegramAction(in Input) Decision {
	if in.Source != Telegram {
		return Decision{Kind: Reject, Code: "not-control-source", Reason: "Account management requires Telegram admission."}
	}
	got := p.Decide(in)
	if got.Kind != Bridge && got.Kind != Reject {
		return Decision{Kind: Reject, Code: "not-action-data", Reason: "Commands are not event action data."}
	}
	return got
}
