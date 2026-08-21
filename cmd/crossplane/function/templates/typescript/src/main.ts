#!/usr/bin/env node

import { Command, type OptionValues } from 'commander';
import {
  newGrpcServer,
  startServer,
  FunctionRunner,
  type ServerOptions,
} from '@crossplane-org/function-sdk-typescript';
import { pino } from 'pino';
import { Function } from './function.js';

const defaultAddress = '0.0.0.0:9443';
const defaultTlsServerCertsDir = '/tls/server';

const program = new Command('function')
  .option('--address <address>', 'Address at which to listen for gRPC connections', defaultAddress)
  .option('-d, --debug', 'Emit debug logs.', false)
  .option('--insecure', 'Run without mTLS credentials.', false)
  .option(
    '--tls-server-certs-dir <directory>',
    'Serve using mTLS certificates in this directory.',
    defaultTlsServerCertsDir
  );

function parseArgs(args: OptionValues): ServerOptions {
  return {
    address: typeof args.address === 'string' ? args.address : defaultAddress,
    debug: Boolean(args.debug),
    insecure: Boolean(args.insecure),
    tlsServerCertsDir:
      typeof args.tlsServerCertsDir === 'string'
        ? args.tlsServerCertsDir
        : defaultTlsServerCertsDir,
  };
}

function main() {
  program.parse(process.argv);
  const args = program.opts();
  const opts = parseArgs(args);

  const logger = pino({
    level: opts?.debug ? 'debug' : 'info',
    formatters: {
      level: (label: string) => {
        return { severity: label.toUpperCase() };
      },
    },
  });

  logger.debug({ options: opts }, 'Starting function');

  try {
    const fn = new Function();
    const fnRunner = new FunctionRunner(fn, logger);
    const server = newGrpcServer(fnRunner, logger);
    startServer(server, opts, logger);

    process.on('SIGINT', () => {
      logger.info('Shutting down gracefully...');
      server.tryShutdown((err: Error | undefined) => {
        if (err) {
          logger.error(err, 'Error during shutdown');
          process.exit(1);
        }
        logger.info('Server shut down successfully');
        process.exit(0);
      });
    });
  } catch (err) {
    logger.error(err);
    process.exit(1);
  }
}

main();
