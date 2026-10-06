import pytest

import whatsapp


def test_bridge_auth_header_comes_from_store_token(tmp_path, monkeypatch):
    database = tmp_path / "store" / "messages.db"
    database.parent.mkdir()
    (database.parent / ".bridge-token").write_text("ab" * 32, encoding="ascii")
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(database))

    assert whatsapp._bridge_auth_headers() == {"Authorization": f"Bearer {'ab' * 32}"}


def test_missing_bridge_token_explains_startup_order(tmp_path, monkeypatch):
    database = tmp_path / "store" / "messages.db"
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(database))

    with pytest.raises(RuntimeError, match="start the Go bridge first"):
        whatsapp._bridge_auth_headers()
