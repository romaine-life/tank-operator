from tank_operator.auth import gravatar_url


def test_gravatar_url_normalizes_email_before_hashing() -> None:
    assert gravatar_url("  USER@Example.COM  ") == (
        "https://www.gravatar.com/avatar/"
        "b58996c504c5638798eb6b511e6f49af?s=64&d=mp"
    )


def test_gravatar_url_allows_explicit_size() -> None:
    assert gravatar_url("user@example.com", size=128).endswith("?s=128&d=mp")
