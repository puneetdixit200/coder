import { act, screen, waitFor } from "@testing-library/react";
import * as apiModule from "#/api/api";
import type { UserSecret, UserSecretEvent } from "#/api/typesGenerated";
import { MockUserOwner, MockUserSecrets } from "#/testHelpers/entities";
import { renderWithAuth } from "#/testHelpers/renderHelpers";
import type {
	OneWayMessageEvent,
	OneWayWebSocketApi,
} from "#/utils/OneWayWebSocket";
import SecretsPage from "./SecretsPage";

class SecretEventsSocket implements OneWayWebSocketApi<UserSecretEvent> {
	readonly url = "ws://example.com/api/v2/users/me/secrets/-/watch";
	readonly listeners = new Map<string, Set<(event: unknown) => void>>();
	closed = false;

	addEventListener: OneWayWebSocketApi<UserSecretEvent>["addEventListener"] = (
		event,
		handler,
	) => {
		const listeners = this.listeners.get(event) ?? new Set();
		listeners.add(handler as (event: unknown) => void);
		this.listeners.set(event, listeners);
	};

	removeEventListener: OneWayWebSocketApi<UserSecretEvent>["removeEventListener"] =
		(event, handler) => {
			this.listeners.get(event)?.delete(handler as (event: unknown) => void);
		};

	close(): void {
		this.closed = true;
	}

	emitOpen(): void {
		for (const listener of this.listeners.get("open") ?? []) {
			listener(new Event("open"));
		}
	}

	emitMessage(event: OneWayMessageEvent<UserSecretEvent>): void {
		for (const listener of this.listeners.get("message") ?? []) {
			listener(event);
		}
	}
}

afterEach(() => {
	vi.restoreAllMocks();
});

const secretWithName = (secret: UserSecret, name: string): UserSecret => ({
	...secret,
	id: name,
	name,
	env_name: `${name.toUpperCase().replaceAll("-", "_")}_ENV`,
	file_path: "",
});

describe(SecretsPage.name, () => {
	it("refetches secrets when the event socket opens", async () => {
		const firstSecret = secretWithName(MockUserSecrets[0], "initial-secret");
		const refreshedSecret = secretWithName(
			MockUserSecrets[1],
			"opened-socket-secret",
		);
		const getUserSecrets = vi
			.spyOn(apiModule.API, "getUserSecrets")
			.mockResolvedValueOnce([firstSecret])
			.mockResolvedValueOnce([refreshedSecret]);
		const socket = new SecretEventsSocket();
		vi.spyOn(apiModule, "watchUserSecrets").mockReturnValue(socket);

		renderWithAuth(<SecretsPage />);

		await screen.findByText(firstSecret.name);
		await waitFor(() => {
			expect(apiModule.watchUserSecrets).toHaveBeenCalledWith(MockUserOwner.id);
		});

		act(() => {
			socket.emitOpen();
		});

		await screen.findByText(refreshedSecret.name);
		expect(getUserSecrets).toHaveBeenCalledTimes(2);
	});

	it("refreshes the secrets query when a matching secret event arrives", async () => {
		const firstSecret = secretWithName(MockUserSecrets[0], "initial-secret");
		const refreshedSecret = secretWithName(
			MockUserSecrets[1],
			"refreshed-secret",
		);
		const getUserSecrets = vi
			.spyOn(apiModule.API, "getUserSecrets")
			.mockResolvedValueOnce([firstSecret])
			.mockResolvedValueOnce([refreshedSecret]);
		const socket = new SecretEventsSocket();
		vi.spyOn(apiModule, "watchUserSecrets").mockReturnValue(socket);

		renderWithAuth(<SecretsPage />);

		await screen.findByText(firstSecret.name);
		await waitFor(() => {
			expect(apiModule.watchUserSecrets).toHaveBeenCalledWith(MockUserOwner.id);
		});

		act(() => {
			socket.emitMessage({
				parseError: undefined,
				parsedMessage: {
					kind: "updated",
					user_id: MockUserOwner.id,
					name: firstSecret.name,
				},
				sourceEvent: new MessageEvent("message"),
			});
		});

		await screen.findByText(refreshedSecret.name);
		expect(getUserSecrets).toHaveBeenCalledTimes(2);
	});

	it("closes the secret event socket on unmount", async () => {
		const secret = secretWithName(MockUserSecrets[0], "socket-cleanup-secret");
		vi.spyOn(apiModule.API, "getUserSecrets").mockResolvedValue([secret]);
		const socket = new SecretEventsSocket();
		vi.spyOn(apiModule, "watchUserSecrets").mockReturnValue(socket);

		const { unmount } = renderWithAuth(<SecretsPage />);
		await screen.findByText(secret.name);

		unmount();

		expect(socket.closed).toBe(true);
	});
});
