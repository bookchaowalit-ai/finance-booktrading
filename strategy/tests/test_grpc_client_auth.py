"""The gRPC client must send the shared service token on every call."""

from unittest.mock import MagicMock

from infrastructure.grpc.grpc_client import GRPCClient


def _client():
    client = GRPCClient()
    client.order_stub = MagicMock()
    client.bot_stub = MagicMock()
    client.market_data_stub = MagicMock()
    client.market_data_stub.SubscribeMarketData.return_value = iter(())
    return client


def _calls(client):
    return [
        client.order_stub.ExecuteOrder,
        client.order_stub.CancelOrder,
        client.bot_stub.GetBotStatus,
        client.bot_stub.StartBot,
        client.bot_stub.StopBot,
        client.market_data_stub.SubscribeMarketData,
    ]


def _exercise(client):
    client.execute_order("BTCTHB", "BUY", 0.001)
    client.cancel_order("1", "BTCTHB")
    client.get_bot_status()
    client.start_bot()
    client.stop_bot()
    client.subscribe_market_data(["BTCTHB"], lambda _data: None)


def test_every_call_sends_bearer_metadata(monkeypatch):
    monkeypatch.setenv("AUTH_TOKEN", "svc-token")
    client = _client()
    _exercise(client)

    for stub_call in _calls(client):
        assert stub_call.called, stub_call
        metadata = stub_call.call_args.kwargs.get("metadata")
        assert metadata == [("authorization", "Bearer svc-token")], stub_call


def test_connect_refuses_without_token(monkeypatch):
    monkeypatch.delenv("AUTH_TOKEN", raising=False)
    channel = MagicMock()
    monkeypatch.setattr("infrastructure.grpc.grpc_client.grpc.insecure_channel", channel)

    assert GRPCClient().connect() is False
    channel.assert_not_called()
