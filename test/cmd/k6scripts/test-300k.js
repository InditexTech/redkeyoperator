// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

/*
  Test that imitates a reredkeydis-cluster that was causing rebalancing errors:
  - Value Size: 1Byte - 300KBytes
  - Timeout: 30s
  - Some deletes
 */

import redis from 'k6/x/redis';
import { randomBytes } from 'k6/crypto';
import { sleep } from 'k6';

const client = new redis.Client({
    cluster: {
        nodes: __ENV.REDIS_HOSTS.split(',').map(node => `redis://${node}`),
    },
});

// Helper function to generate random-sized values
function generateRandomValue(maxBytes) {
    const size = Math.floor(Math.random() * maxBytes) + 1; // Random size from 1 to maxBytes
    return randomBytes(size).toString('base64');
}

export default function () {
    const uniqueKey = `mykey_${__VU}_${__ITER}`;
    const value = generateRandomValue(300000);

    // Set the key with a TTL of 30 seconds
    client.set(uniqueKey, value, 30);

    // Randomly delete approximately 1 in 10 keys
    if (Math.random() < 0.1) {
        client.del(uniqueKey).then((deleted) => {
            if (deleted === 1) {
                console.log(`Key "${uniqueKey}" deleted.`);
            } else {
                console.warn(`Failed to delete key "${uniqueKey}".`);
            }
        }).catch((error) => {
            console.error(`Error deleting key "${uniqueKey}": ${error}`);
        });
    } else {
        // Retrieve the value for the key
        client.get(uniqueKey).then((retrievedValue) => {
            if (retrievedValue === null) {
                console.error(`Key "${uniqueKey}" not found!`);
            } else {
                console.log(`Retrieved value for key "${uniqueKey}": Length=${retrievedValue.length}`);
            }
        }).catch((error) => {
            console.error(`Error retrieving key "${uniqueKey}": ${error}`);
        });
    }

    // Sleep for a short random duration to simulate real-world load patterns
    sleep(Math.random() * 0.1); // Sleep for up to 100ms
}
