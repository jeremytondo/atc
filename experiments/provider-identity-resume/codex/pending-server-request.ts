import {
  type JsonObject,
  type ServerRequest,
  type ServerRequestHandler,
} from "./app-server-client.ts";

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (error: Error) => void;
}

export class PendingServerRequest {
  readonly expectedMethod: string;
  readonly handle: ServerRequestHandler;

  private readonly requestDeferred = deferred<ServerRequest>();
  private readonly responseDeferred = deferred<JsonObject>();
  private captured?: ServerRequest;
  private settled = false;

  constructor(expectedMethod: string) {
    this.expectedMethod = expectedMethod;
    this.handle = async (request) => {
      if (request.method !== this.expectedMethod) {
        throw new Error(
          `Expected server request ${this.expectedMethod}, received ${request.method}`,
        );
      }
      if (this.captured !== undefined) {
        throw new Error(
          `Received a second ${this.expectedMethod} server request`,
        );
      }
      this.captured = request;
      this.requestDeferred.resolve(request);
      return await this.responseDeferred.promise;
    };
  }

  async wait(timeoutMs: number): Promise<ServerRequest> {
    return await withTimeout(
      this.requestDeferred.promise,
      timeoutMs,
      `server request ${this.expectedMethod}`,
    );
  }

  respond(result: JsonObject): void {
    this.ensurePending();
    this.settled = true;
    this.responseDeferred.resolve(result);
  }

  reject(message: string): void {
    this.ensurePending();
    this.settled = true;
    this.responseDeferred.reject(new Error(message));
  }

  private ensurePending(): void {
    if (this.captured === undefined) {
      throw new Error(
        `Cannot settle ${this.expectedMethod} before it is received`,
      );
    }
    if (this.settled) {
      throw new Error(`${this.expectedMethod} was already settled`);
    }
  }
}

function deferred<T>(): Deferred<T> {
  let resolvePromise!: (value: T) => void;
  let rejectPromise!: (error: Error) => void;
  const promise = new Promise<T>((resolve, reject) => {
    resolvePromise = resolve;
    rejectPromise = reject;
  });
  return {
    promise,
    resolve: resolvePromise,
    reject: rejectPromise,
  };
}

async function withTimeout<T>(
  promise: Promise<T>,
  timeoutMs: number,
  description: string,
): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  try {
    return await Promise.race([
      promise,
      new Promise<never>((_, reject) => {
        timer = setTimeout(
          () =>
            reject(
              new Error(
                `Timed out after ${timeoutMs}ms waiting for ${description}`,
              ),
            ),
          timeoutMs,
        );
      }),
    ]);
  } finally {
    if (timer !== undefined) {
      clearTimeout(timer);
    }
  }
}
