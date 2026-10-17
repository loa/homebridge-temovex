import type { API, Characteristic, DynamicPlatformPlugin, Logging, PlatformAccessory, PlatformConfig, Service } from 'homebridge';

import { temperatureDevices } from './devices.js';
import { TemperatureAccessory } from './temperatureAccessory.js';
import { ThermostatAccessory } from './thermostatAccessory.js';
import { failureText, parseProxyUrl, ProxyClient, proxyLabel } from './proxy.js';
import { PLATFORM_NAME, PLUGIN_NAME } from './settings.js';

export class TemovexPlatform implements DynamicPlatformPlugin {
  public readonly Service: typeof Service;
  public readonly Characteristic: typeof Characteristic;
  public readonly accessories: Map<string, PlatformAccessory> = new Map();
  public readonly client?: ProxyClient;

  constructor(
    public readonly log: Logging,
    public readonly config: PlatformConfig,
    public readonly api: API,
  ) {
    this.Service = api.hap.Service;
    this.Characteristic = api.hap.Characteristic;

    try {
      const url = parseProxyUrl(this.config.url);
      this.client = new ProxyClient(url);
      this.log.info('Temovex proxy:', proxyLabel(url));
    } catch (error) {
      this.log.error(failureText(error));
    }

    this.api.on('didFinishLaunching', () => {
      this.discoverDevices();
    });
  }

  configureAccessory(accessory: PlatformAccessory): void {
    this.log.info('Loading accessory from cache:', accessory.displayName);
    this.accessories.set(accessory.UUID, accessory);
  }

  discoverDevices(): void {
    const client = this.client;
    if (!client) {
      return;
    }
    const seen = new Set<string>();
    for (const device of temperatureDevices) {
      const uuid = this.api.hap.uuid.generate(`temovex:${device.name}`);
      seen.add(uuid);
      const existing = this.accessories.get(uuid);
      if (existing) {
        existing.context.device = device;
        if (existing.displayName !== device.description) {
          existing.displayName = device.description;
          this.api.updatePlatformAccessories([existing]);
        }
        this.log.info('Restoring existing accessory from cache:', existing.displayName);
        this.attach(existing, client);
      } else {
        this.log.info('Adding new accessory:', device.description);
        const category = device.kind === 'thermostat'
          ? this.api.hap.Categories.THERMOSTAT
          : this.api.hap.Categories.SENSOR;
        const accessory = new this.api.platformAccessory(device.description, uuid, category);
        accessory.context.device = device;
        this.attach(accessory, client);
        this.api.registerPlatformAccessories(PLUGIN_NAME, PLATFORM_NAME, [accessory]);
      }
    }
    for (const [uuid, accessory] of this.accessories) {
      if (!seen.has(uuid)) {
        this.log.info('Removing existing accessory from cache:', accessory.displayName);
        this.api.unregisterPlatformAccessories(PLUGIN_NAME, PLATFORM_NAME, [accessory]);
      }
    }
  }

  private attach(accessory: PlatformAccessory, client: ProxyClient): void {
    if (accessory.context.device.kind === 'thermostat') {
      new ThermostatAccessory(this, accessory, client);
      return;
    }
    new TemperatureAccessory(this, accessory, client);
  }
}
