import React, { useState, useEffect, useRef } from 'react';
import styled from 'styled-components';
import { nip19, getPublicKey, getEventHash, finishEvent, signEvent } from 'nostr-tools';
import { getDecodedToken } from '@cashu/cashu-ts';
import Background from './components/Background';
import './App.css';

// Import the TollGate logos
import logoWhite from './assets/logo/TollGate_Logo-C-white.png';
import iconTransparent from './assets/logo/TollGate_icon-trannsparent.png';

// Utility function to get current timestamp for Nostr
const nostrNow = () => Math.floor(Date.now() / 1000);

// Safely extract proofs from a decoded token
const extractProofsFromToken = (decodedToken) => {
  const proofs = [];
  
  try {
    // Handle token array format
    if (decodedToken.token && Array.isArray(decodedToken.token)) {
      decodedToken.token.forEach(t => {
        if (t.proofs && Array.isArray(t.proofs)) {
          proofs.push(...t.proofs);
        }
      });
    }
    // Handle single token format
    else if (decodedToken.token && decodedToken.token.proofs) {
      proofs.push(...decodedToken.token.proofs);
    }
    // Handle direct token structure
    else if (decodedToken.proofs && Array.isArray(decodedToken.proofs)) {
      proofs.push(...decodedToken.proofs);
    }
    // Handle token with tokens array
    else if (decodedToken.tokens && Array.isArray(decodedToken.tokens)) {
      decodedToken.tokens.forEach(t => {
        if (t.proofs && Array.isArray(t.proofs)) {
          proofs.push(...t.proofs);
        }
      });
    }
  } catch (error) {
    console.error('Error extracting proofs:', error);
  }
  
  return proofs;
};

// Helper function to format data size in user-friendly units
const formatDataSize = (kibiBytes) => {
  if (kibiBytes < 1024) {
    return {
      value: kibiBytes,
      unit: 'KiB'
    };
  } else if (kibiBytes < 1048576) { // Less than 1 GB
    return {
      value: (kibiBytes / 1024).toFixed(1),
      unit: 'MB'
    };
  } else {
    return {
      value: (kibiBytes / 1048576).toFixed(2),
      unit: 'GB'
    };
  }
};

// Helper function to format time in seconds to appropriate units
const formatTimeInSeconds = (seconds) => {
  if (seconds < 60) {
    return {
      value: Math.round(seconds),
      unit: seconds === 1 ? 'second' : 'seconds'
    };
  } else if (seconds < 3600) {
    const minutes = seconds / 60;
    return {
      value: minutes.toFixed(1),
      unit: minutes === 1 ? 'minute' : 'minutes'
    };
  } else {
    const hours = seconds / 3600;
    return {
      value: hours.toFixed(2),
      unit: hours === 1 ? 'hour' : 'hours'
    };
  }
};

function App() {
  const [token, setToken] = useState('');
  const [tokenValue, setTokenValue] = useState(null);
  const [tokenError, setTokenError] = useState('');
  const [status, setStatus] = useState('');
  const [tollgateDetails, setTollgateDetails] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [deviceInfo, setDeviceInfo] = useState(null);
  const [retrying, setRetrying] = useState(false);
  const retryIntervalRef = useRef(null);
  const [showSuccess, setShowSuccess] = useState(false);
  const [showCloseButton, setShowCloseButton] = useState(false);
  const [closeButtonClicked, setCloseButtonClicked] = useState(false);
  const [isInputFocused, setIsInputFocused] = useState(false);

  // Get the base URL dynamically
  const getTollgateBaseUrl = () => {
    // Get the current host (without port)
    const currentHost = window.location.hostname;
    // const currentHost = "192.168.1.1";
    // Always use port 2121 as specified in the TollGate spec
    return `http://${currentHost}:2121`;
  };

  // Function to fetch the TollGate details and device info
  const fetchTollgateData = async () => {
    try {
      const baseUrl = getTollgateBaseUrl();
      
      // Fetch TollGate details
      const detailsResponse = await fetch(`${baseUrl}/`);
      
      if (!detailsResponse.ok) {
        throw new Error(`Failed to fetch TollGate details: ${detailsResponse.status}`);
      }
      
      const detailsEvent = await detailsResponse.json();
      setTollgateDetails(detailsEvent);
      
      // Fetch device MAC address
      const whoamiResponse = await fetch(`${baseUrl}/whoami`);
      
      if (!whoamiResponse.ok) {
        throw new Error(`Failed to fetch device info: ${whoamiResponse.status}`);
      }
      
      const whoamiText = await whoamiResponse.text();
      // Parse format like 'mac=00:11:22:33:44:55'
      const [identifierType, identifierValue] = whoamiText.trim().split('=');
      
      setDeviceInfo({
        type: identifierType,
        value: identifierValue
      });
      
      // Clear error on success
      setError('');
      setRetrying(false);
      
      // Clear any existing retry interval
      if (retryIntervalRef.current) {
        clearInterval(retryIntervalRef.current);
        retryIntervalRef.current = null;
      }
      
      return true;
    } catch (err) {
      console.error('Error fetching TollGate data:', err);
      setError('Could not fetch TollGate information. Retrying...');
      return false;
    }
  };

  // Initial data fetch on mount
  useEffect(() => {
    const initialFetch = async () => {
      setLoading(true);
      const success = await fetchTollgateData();
      
      if (!success) {
        setRetrying(true);
      }
      
      setLoading(false);
    };
    
    initialFetch();
    
    // Cleanup on unmount
    return () => {
      if (retryIntervalRef.current) {
        clearInterval(retryIntervalRef.current);
      }
    };
  }, []);
  
  // Set up retry mechanism when there's an error
  useEffect(() => {
    // Only set up retry if there's an error and no existing retry interval
    if (!error || tollgateDetails || retryIntervalRef.current) {
      return;
    }
    
    console.log('Setting up retry interval for TollGate details');
    setRetrying(true);
    
    // Set up the retry interval
    retryIntervalRef.current = setInterval(async () => {
      console.log('Retrying to fetch TollGate details...');
      const success = await fetchTollgateData();
      
      // If successful, clear the interval
      if (success) {
        clearInterval(retryIntervalRef.current);
        retryIntervalRef.current = null;
        setRetrying(false);
      }
    }, 5000);
    
    // Cleanup function
    return () => {
      if (retryIntervalRef.current) {
        clearInterval(retryIntervalRef.current);
        retryIntervalRef.current = null;
      }
    };
  }, [error, tollgateDetails]);

  // Add an effect to set viewport meta tag for better mobile handling
  useEffect(() => {
    // Create or update viewport meta tag to prevent automatic zooming on inputs
    let viewportMeta = document.querySelector('meta[name="viewport"]');
    if (!viewportMeta) {
      viewportMeta = document.createElement('meta');
      viewportMeta.name = 'viewport';
      document.head.appendChild(viewportMeta);
    }
    viewportMeta.content = 'width=device-width, initial-scale=1, maximum-scale=1, user-scalable=no';
    
    return () => {
      // Restore default viewport when component unmounts
      if (viewportMeta) {
        viewportMeta.content = 'width=device-width, initial-scale=1';
      }
    };
  }, []);

  // Helper function to get pricing info
  const getPricingInfo = () => {
    if (!tollgateDetails || !tollgateDetails.tags) return null;
    
    const metric = tollgateDetails.tags.find(tag => tag[0] === 'metric');
    const stepSize = tollgateDetails.tags.find(tag => tag[0] === 'step_size');
    const pricePerStep = tollgateDetails.tags.find(tag => tag[0] === 'price_per_step');
    const mints = tollgateDetails.tags.filter(tag => tag[0] === 'mint').map(mint => mint[1]);
    
    if (!metric || !stepSize || !pricePerStep || !mints) return null;
    
    return {
      metric: metric[1],
      stepSize: Number(stepSize[1]),
      price: Number(pricePerStep[1]),
      unit: pricePerStep[2],
      mints: mints
    };
  };
  
  // Update the pricing format to be more digestible
  const formatPricingInfo = () => {
    const pricing = getPricingInfo();
    if (!pricing) return 'Pricing information not available';
    
    // Calculate rate based on step_size and price
    const stepSizeValue = parseFloat(pricing.stepSize);
    const priceValue = parseFloat(pricing.price);
    
    if (pricing.metric.toLowerCase() === 'milliseconds') {
      // Convert milliseconds to minutes for more user-friendly display
      const minutesPerStep = stepSizeValue / 60000;
      if (minutesPerStep < 1) {
        const secondsPerStep = stepSizeValue / 1000;
        return `${priceValue} sats per ${secondsPerStep.toFixed(1)} seconds`;
      } else if (minutesPerStep < 60) {
        return `${priceValue} sats per ${minutesPerStep.toFixed(1)} minute${minutesPerStep === 1 ? '' : 's'}`;
      } else {
        const hoursPerStep = minutesPerStep / 60;
        return `${priceValue} sats per ${hoursPerStep.toFixed(1)} hour${hoursPerStep === 1 ? '' : 's'}`;
      }
    } else if (pricing.metric.toLowerCase() === 'bytes') {
      // For data, format to the most appropriate unit
      const formattedSize = formatDataSize(stepSizeValue);
      return `${formattedSize.value} ${formattedSize.unit} for ${priceValue} sats`;
    }
  };

  // Helper function to calculate purchased allocation
  const calculatePurchasedAllocation = () => {
    if (!tokenValue || !tokenValue.hasProofs || !tokenValue.amount || !tollgateDetails) {
      return null;
    }

    const pricing = getPricingInfo();

    if (!pricing) {
      return null;
    }
    
    // Calculate total allocation: (token_amount / price) * step_size
    const totalSteps = tokenValue.amount / pricing.price;
    const totalAllocation = totalSteps * pricing.stepSize;
 
    if (pricing.metric === 'milliseconds') {
      // Convert milliseconds to minutes or seconds for display
      if (totalAllocation < 60000) {
        // If less than a minute, show in seconds
        const seconds = totalAllocation / 1000;
        return {
          amount: seconds.toFixed(1),
          metric: seconds === 1 ? 'second' : 'seconds',
          rawMetric: pricing.metric,
          rawAmount: Math.round(totalAllocation) // Keep the raw amount in ms for calculations
        };
      } else {
        // If more than a minute, show in minutes
        const minutes = totalAllocation / 60000;
        return {
          amount: minutes.toFixed(1),
          metric: minutes === 1 ? 'minute' : 'minutes',
          rawMetric: pricing.metric,
          rawAmount: Math.round(totalAllocation) // Keep the raw amount in ms for calculations
        };
      }
    } else if (pricing.metric === 'bytes') {
      // For data, format to the most appropriate unit
      const formattedData = formatDataSize(Math.round(totalAllocation));
      return {
        amount: formattedData.value,
        type: formattedData.unit,
        rawMetric: pricing.metric,
        rawAmount: Math.round(totalAllocation) // Keep the raw amount in KiB for calculations
      };
    }
  };

  // When token is pasted, extract proofs and calculate total value
  const handleTokenChange = (e) => {
    const value = e.target.value;
    setToken(value);
    
    // Reset states
    setTokenValue(null);
    setTokenError('');
    
    if (!value.trim()) return;
    
    // Basic validation - Cashu tokens should start with "cashu"
    if (!value.trim().startsWith('cashu')) {
      setTokenError('Invalid token format. Cashu tokens should start with "cashu"');
      return;
    }
    
    try {
      // Attempt to decode the token using the getDecodedToken method
      const decodedToken = getDecodedToken(value.trim());
      
      if (!decodedToken) {
        setTokenError('Could not decode token');
        return;
      }
      
      // Extract proofs from the token
      const proofs = extractProofsFromToken(decodedToken);
      
      if (!proofs || proofs.length === 0) {
        // If we couldn't extract proofs, still accept the token but show no value
        setTokenValue({
          isValid: true,
          hasProofs: false
        });
        return;
      }
      
      // Calculate the sum of proof values
      const totalAmount = proofs.reduce((sum, proof) => {
        const proofAmount = Number(proof.amount || 0);
        return sum + proofAmount;
      }, 0);
      
      // Set the token value with proof details
      setTokenValue({
        isValid: true,
        hasProofs: true,
        amount: totalAmount,
        proofCount: proofs.length,
        unit: 'sats'  // Add unit here
      });
    } catch (error) {
      console.error('Error decoding token:', error);
      setTokenError(error.message || 'Invalid token format');
    }
  };

  // Handle manual page close
  const handleClosePage = () => {
    // Set that the close button was clicked
    setCloseButtonClicked(true);
    
    // Try closing the window
    window.close();
    
    // If we're still here after a short delay, the close failed
    setTimeout(() => {
      // No need to show alert anymore since we'll show the message in UI
      // The fact that this code runs means the window wasn't closed
    }, 300);
  };

  // Update the handleSendToken function
  const handleSendToken = async () => {
    try {
      setStatus('Sending token...');
      
      if (!deviceInfo || !tollgateDetails) {
        throw new Error('Missing device information or TollGate details');
      }
      
      // Get TollGate pubkey from event
      const tollgatePubkey = tollgateDetails.pubkey;
      
      // Generate a random private key for signing
      const privateKeyBytes = window.crypto.getRandomValues(new Uint8Array(32));
      const privateKeyHex = Array.from(privateKeyBytes)
        .map(b => b.toString(16).padStart(2, '0'))
        .join('');
      
      // Generate the pubkey from private key using nostr-tools
      let pubkey;
      try {
        pubkey = getPublicKey(privateKeyHex);
      } catch (e) {
        console.error('Error getting public key:', e);
        throw new Error('Failed to generate keys for signing');
      }
      
      // Create the Nostr event according to TIP-01 spec
      const unsignedEvent = {
        kind: 21000,
        pubkey: pubkey,
        content: "",
        created_at: nostrNow(),
        tags: [
          ["p", tollgatePubkey],
          ["device-identifier", deviceInfo.type, deviceInfo.value],
          ["payment", token],
        ],
      };
      
      // Calculate the event hash (id)
      const id = getEventHash(unsignedEvent);
      
      // Sign the event using signEvent, which is still available
      const sig = signEvent(unsignedEvent, privateKeyHex);
      
      // Create a clean event object for sending
      const event = {
        ...unsignedEvent,
        id,
        sig
      };
      
      console.log('Sending signed event:', event);
      
      // Send the event to the TollGate server
      const baseUrl = getTollgateBaseUrl();
      const response = await fetch(`${baseUrl}/`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify(event),
      });
      
      if (!response.ok) {
        if (response.status === 402) {
          throw new Error('Payment required. Your token was not accepted.');
        } else {
          throw new Error(`Server error: ${response.status}`);
        }
      }
      
      // Success!
      setStatus('');
      setShowSuccess(true);
      
      // Try to auto-close the page after 3 seconds
      setTimeout(() => {
        window.close();
        // If we're still here after a short delay, show the close button
        setTimeout(() => {
          setShowCloseButton(true);
        }, 500);
      }, 3000);
      
      // Reset form
      setToken('');
      setTokenValue(null);
      
    } catch (error) {
      console.error('Error sending token:', error);
      setStatus(`Error: ${error.message}`);
    }
  };

  return (
    <div className="App">
      <Background />
      <Container $isInputFocused={isInputFocused}>
        <LogoContainer>
          <LogoTextImage src={logoWhite} alt="TollGate Logo" />
        </LogoContainer>
        <Card>
          {showSuccess ? (
            <SuccessContainer>
              <SuccessCheckmark>
                <SuccessCheckmarkCircle />
                <CheckmarkPath />
              </SuccessCheckmark>
              <SuccessMessage>
                <h2>Success!</h2>
                <p>You now have internet access.</p>
                {!showCloseButton ? (
                  <p className="small">This window will close automatically.</p>
                ) : (
                  <>
                    <CloseButton onClick={handleClosePage} disabled={closeButtonClicked}>
                      Close This Window
                    </CloseButton>
                    {closeButtonClicked && (
                      <CloseFailureMessage>
                        Your device doesn't allow automatic window closing.
                        Please close this window manually.
                      </CloseFailureMessage>
                    )}
                  </>
                )}
              </SuccessMessage>
            </SuccessContainer>
          ) : (
            <>
              <CardHeader>
                <h2>Internet Access</h2>
                <p>Paste your Cashu token to access the internet</p>
                {!loading && !error && tollgateDetails && (
                  <RateInfo>
                    Rate: {formatPricingInfo()}
                  </RateInfo>
                )}
                {loading && (
                  <LoadingText>
                    <Spinner />
                    Loading pricing details...
                  </LoadingText>
                )}
                {error && (
                  <ErrorText>
                    {error}
                    {retrying && <RetrySpinner />}
                  </ErrorText>
                )}
              </CardHeader>
              
              <InputGroup>
                <Label htmlFor="token">Cashu Token</Label>
                <InputWithButton>
                  <TokenInput 
                    id="token"
                    value={token}
                    onChange={handleTokenChange}
                    placeholder="Paste your Cashu token here"
                    onFocus={() => setIsInputFocused(true)}
                    onBlur={() => setIsInputFocused(false)}
                  />
                  <PasteButton 
                    onClick={async () => {
                      try {
                        const text = await navigator.clipboard.readText();
                        if (text) {
                          setToken(text);
                          handleTokenChange({ target: { value: text } });
                        }
                      } catch (err) {
                        console.error('Failed to read clipboard:', err);
                        setTokenError('Could not access clipboard. Please paste manually.');
                      }
                    }}
                    title="Paste from clipboard"
                  >
                    Paste
                  </PasteButton>
                </InputWithButton>
              </InputGroup>
              
              {tokenValue && (
                <TokenValueDisplay $valid={true}>
                  <ValueRow>
                    <div className="left">
                      <CheckIcon>✓</CheckIcon>
                      <h4>Valid cash</h4>
                    </div>
                    {tokenValue.hasProofs && (
                      <Amount>{tokenValue.amount} sats</Amount>
                    )}
                  </ValueRow>
                  
                  {tokenValue.hasProofs && tollgateDetails && (
                    <PurchaseSummaryInline $valid={true}>
                      {(() => {
                        const allocation = calculatePurchasedAllocation();
                        if (!allocation) return null;
                        
                        return (
                          <>You'll get <strong>{allocation.amount} {allocation.metric}</strong> of internet access</>
                        );
                      })()}
                    </PurchaseSummaryInline>
                  )}
                </TokenValueDisplay>
              )}
              
              {tokenError && (
                <TokenValueDisplay $valid={false}>
                  <ValueHeader>
                    <ErrorIcon>✗</ErrorIcon>
                    <h4>Invalid Token</h4>
                  </ValueHeader>
                  <p>{tokenError}</p>
                </TokenValueDisplay>
              )}
              
              <Button 
                onClick={handleSendToken}
                disabled={!tokenValue}
              >
                {(() => {
                  const allocation = calculatePurchasedAllocation();
                  if (!allocation) return "Purchase Internet Access";
                  
                  // For minutes, use "minute" or "minutes" based on quantity
                  if (allocation.rawMetric === 'min') {
                    const unit = allocation.amount === 1 ? 'minute' : 'minutes';
                    return `Purchase ${allocation.amount} ${unit}`;
                  } else if (allocation.rawMetric === 'sec') {
                    // We've already formatted seconds into appropriate units in calculatePurchasedAllocation
                    return `Purchase ${allocation.amount} ${allocation.metric}`;
                  } else {
                    return `Purchase ${allocation.amount} ${allocation.metric}`;
                  }
                })()}
              </Button>
              
              {status && !showSuccess && (
                <StatusMessage>
                  {status}
                  {status.includes('Sending') && <Spinner style={{ marginLeft: '10px' }} />}
                </StatusMessage>
              )}
              
              {deviceInfo && (
                <DeviceInfo>
                  Your device: {deviceInfo.type}={deviceInfo.value}
                </DeviceInfo>
              )}
            </>
          )}
        </Card>
        <Footer>
          <p>Powered by <strong>TollGate</strong> - Pay-as-you-go internet access</p>
        </Footer>
      </Container>
    </div>
  );
}

// Styled Components
const Container = styled.div`
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  position: relative;
  z-index: 1;
  padding: 15px;
  overflow-y: auto;
  
  @media (max-width: 768px) {
    padding: 10px;
    justify-content: ${props => props.$isInputFocused ? 'flex-start' : 'center'};
    height: ${props => props.$isInputFocused ? 'auto' : '100vh'};
    
    /* When input is focused (keyboard is visible), adjust layout */
    ${props => props.$isInputFocused && `
      min-height: auto;
      height: auto;
    `}
  }
`;

const LogoContainer = styled.div`
  display: flex;
  justify-content: center;
  margin-bottom: 20px;
  
  @media (max-width: 768px) {
    margin-bottom: 15px;
  }
`;

const LogoTextImage = styled.img`
  height: 50px;
  filter: drop-shadow(0 0 10px rgba(0, 0, 0, 0.5));
  
  @media (max-width: 768px) {
    height: 40px;
  }
`;

const Logo = styled.h1`  color: white;
  margin: 0;
  font-weight: 700;
  text-shadow: 0 0 10px rgba(0, 0, 0, 0.5);
`;

const Card = styled.div`
  background: rgba(255, 255, 255, 0.9);
  border-radius: 12px;
  padding: 25px;
  width: 100%;
  max-width: 500px;
  box-shadow: 0 10px 25px rgba(0, 0, 0, 0.1);
  
  @media (max-width: 768px) {
    padding: 15px;
    border-radius: 10px;
  }
`;

const CardHeader = styled.div`
  text-align: center;
  margin-bottom: 15px;
  
  h2 {
    color: #333;
    margin-top: 0;
    margin-bottom: 8px;
    font-size: 24px;
    
    @media (max-width: 768px) {
      font-size: 20px;
      margin-bottom: 6px;
    }
  }
  
  p {
    color: #666;
    margin-bottom: 0;
    
    @media (max-width: 768px) {
      font-size: 14px;
    }
  }
`;

const InputGroup = styled.div`
  margin-bottom: 15px;
  
  @media (max-width: 768px) {
    margin-bottom: 12px;
  }
`;

const Label = styled.label`
  display: block;
  margin-bottom: 6px;
  font-weight: 600;
  color: #444;
  
  @media (max-width: 768px) {
    font-size: 14px;
    margin-bottom: 4px;
  }
`;

const InputWithButton = styled.div`
  display: flex;
  align-items: stretch;
  width: 100%;
`;

const TokenInput = styled.input`
  flex: 1;
  padding: 12px;
  border: 1px solid #ddd;
  border-radius: 6px 0 0 6px;
  font-size: 16px;
  box-sizing: border-box;
  
  &:focus {
    outline: none;
    border-color: #4f46e5;
    box-shadow: 0 0 0 2px rgba(79, 70, 229, 0.2);
  }
`;

const PasteButton = styled.button`
  background-color: #f7b44c;
  color: #1a1f38;
  border: none;
  border-radius: 0 6px 6px 0;
  padding: 0 15px;
  font-size: 16px;
  font-weight: 600;
  cursor: pointer;
  transition: background-color 0.2s;
  white-space: nowrap;
  display: flex;
  align-items: center;
  gap: 4px;
  
  span {
    font-size: 18px;
  }
  
  &:hover {
    background-color: #e9a53d;
  }
  
  &:active {
    background-color: #d99b35;
  }
`;

const Button = styled.button`
  background-color: #f7b44c;
  color: #1a1f38;
  border: none;
  border-radius: 12px;
  padding: 16px 20px;
  font-size: 18px;
  font-weight: 600;
  cursor: pointer;
  width: 100%;
  transition: background-color 0.2s;
  
  @media (max-width: 768px) {
    padding: 12px 16px;
    font-size: 16px;
    border-radius: 8px;
  }
  
  &:hover {
    background-color: #e9a53d;
  }
  
  &:disabled {
    background-color: #d4d4d4;
    cursor: not-allowed;
    color: #808080;
  }
`;

const TokenValueDisplay = styled.div`
  background-color: ${props => props.$valid ? '#f0f9ff' : '#fff1f0'};
  border: 1px solid ${props => props.$valid ? '#bae6fd' : '#fecaca'};
  border-radius: 6px;
  padding: 12px;
  margin-bottom: 15px;
  
  h4 {
    margin: 0;
    color: ${props => props.$valid ? '#0369a1' : '#dc2626'};
    
    @media (max-width: 768px) {
      font-size: 14px;
    }
  }
  
  p {
    margin: 8px 0 0 0;
    color: #666;
    
    @media (max-width: 768px) {
      font-size: 13px;
      margin-top: 5px;
    }
  }
  
  @media (max-width: 768px) {
    padding: 8px;
    margin-bottom: 12px;
    border-radius: 5px;
  }
`;

const ValueRow = styled.div`
  display: flex;
  align-items: center;
  justify-content: space-between;
  
  .left {
    display: flex;
    align-items: center;
  }
`;

const Amount = styled.span`
  font-weight: 600;
  color: #0369a1;
  font-size: 16px;
  
  @media (max-width: 768px) {
    font-size: 14px;
  }
`;

const ValueHeader = styled.div`
  display: flex;
  align-items: center;
`;

const CheckIcon = styled.div`
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  background-color: #0d9488;
  color: white;
  border-radius: 50%;
  margin-right: 8px;
  font-size: 14px;
  
  @media (max-width: 768px) {
    width: 20px;
    height: 20px;
    font-size: 12px;
    margin-right: 6px;
  }
`;

const ErrorIcon = styled.div`
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  background-color: #dc2626;
  color: white;
  border-radius: 50%;
  margin-right: 8px;
  font-size: 14px;
  
  @media (max-width: 768px) {
    width: 20px;
    height: 20px;
    font-size: 12px;
    margin-right: 6px;
  }
`;

const ValueDetails = styled.div`
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid #bae6fd;
`;

const ValueItem = styled.div`
  display: flex;
  justify-content: space-between;
  margin-bottom: 6px;
  font-size: 14px;
  
  span {
    color: #666;
  }
  
  strong {
    color: #0369a1;
  }
`;

const ValueNote = styled.div`
  margin-top: 8px;
  font-size: 12px;
  color: #666;
  font-style: italic;
  text-align: center;
`;

const StatusMessage = styled.div`
  margin-top: 20px;
  padding: 10px;
  border-radius: 6px;
  background-color: #f3f4f6;
  color: #4f46e5;
  font-weight: 500;
  text-align: center;
  display: flex;
  align-items: center;
  justify-content: center;
`;

const Footer = styled.div`
  margin-top: 20px;
  text-align: center;
  
  p {
    color: rgba(255, 255, 255, 0.8);
    font-size: 14px;
    margin: 0;
  }
  
  strong {
    color: white;
  }
  
  @media (max-width: 768px) {
    margin-top: 15px;
    
    p {
      font-size: 12px;
    }
  }
`;

// New styled components for pricing info
const RateInfo = styled.div`
  display: inline-block;
  background-color: rgba(79, 70, 229, 0.1);
  border-radius: 20px;
  padding: 4px 12px;
  margin-top: 10px;
  font-size: 14px;
  color: #4f46e5;
  font-weight: 500;
`;

const LoadingText = styled.div`
  margin-top: 10px;
  font-size: 14px;
  color: #666;
  font-style: italic;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
`;

const Spinner = styled.div`
  width: 16px;
  height: 16px;
  border: 2px solid rgba(79, 70, 229, 0.2);
  border-top: 2px solid #4f46e5;
  border-radius: 50%;
  animation: spin 1s linear infinite;
  
  @keyframes spin {
    0% { transform: rotate(0deg); }
    100% { transform: rotate(360deg); }
  }
`;

const RetrySpinner = styled(Spinner)`
  border: 2px solid rgba(220, 38, 38, 0.2);
  border-top: 2px solid #dc2626;
  margin-left: 8px;
`;

const ErrorText = styled.div`
  margin-top: 10px;
  font-size: 14px;
  color: #dc2626;
  font-style: italic;
  display: flex;
  align-items: center;
  justify-content: center;
`;

const PurchaseSummary = styled.div`
  text-align: center;
  margin: 16px 0;
  color: #4b5563;
  font-size: 15px;
  
  strong {
    color: #0369a1;
  }
  
  @media (max-width: 768px) {
    margin: 10px 0;
    font-size: 14px;
  }
`;

// New styled component for device info
const DeviceInfo = styled.div`
  text-align: center;
  margin-top: 10px;
  font-size: 12px;
  color: #666;
  
  @media (max-width: 768px) {
    font-size: 11px;
    margin-top: 8px;
  }
`;

const SuccessContainer = styled.div`
  text-align: center;
  padding: 20px 0;
`;

const SuccessCheckmark = styled.div`  width: 80px;
  height: 80px;
  position: relative;
  margin: 20px auto 30px;
`;

const SuccessCheckmarkCircle = styled.div`
  position: absolute;
  width: 80px;
  height: 80px;
  border-radius: 50%;
  border: 4px solid #4caf50;
  box-sizing: border-box;
  transform-origin: center;
  animation: circle-animation 0.3s ease-in-out forwards;

  @keyframes circle-animation {
    0% {
      transform: scale(0);
      opacity: 0;
    }
    100% {
      transform: scale(1);
      opacity: 1;
    }
  }
`;

// Use CSS to create a better checkmark with pseudo-elements
const CheckmarkPath = styled.div`
  position: absolute;
  top: 50%;
  left: 50%;
  width: 40px;
  height: 40px;
  transform: translate(-50%, -50%) scale(0);
  animation: checkmark-animation 0.3s ease-in-out 0.3s forwards;
  
  &:before, &:after {
    content: '';
    position: absolute;
    background-color: #4caf50;
    border-radius: 4px;
    opacity: 0;
  }
  
  /* The short part of the checkmark (left side) */
  &:before {
    width: 15px;
    height: 6px;
    left: 6px;
    top: 22px;
    transform: rotate(45deg);
    animation: short-check 0.15s ease-in-out 0.45s forwards;
  }
  
  /* The long part of the checkmark (right side) */
  &:after {
    width: 26px;
    height: 6px;
    left: 14px;
    top: 18px;
    transform: rotate(-45deg);
    animation: long-check 0.15s ease-in-out 0.3s forwards;
  }
  
  @keyframes checkmark-animation {
    0% {
      transform: translate(-50%, -50%) scale(0);
    }
    50% {
      transform: translate(-50%, -50%) scale(1.2);
    }
    100% {
      transform: translate(-50%, -50%) scale(1);
    }
  }
  
  @keyframes short-check {
    0% {
      width: 0;
      opacity: 1;
    }
    100% {
      width: 15px;
      opacity: 1;
    }
  }
  
  @keyframes long-check {
    0% {
      width: 0;
      opacity: 1;
    }
    100% {
      width: 26px;
      opacity: 1;
    }
  }
`;

const SuccessMessage = styled.div`
  margin-top: 20px;
  padding: 10px;
  border-radius: 6px;
  background-color: #f0f9ff;
  color: #4caf50;
  font-weight: 500;
  text-align: center;
  
  h2 {
    margin-top: 0;
    margin-bottom: 10px;
    font-size: 24px;
    color: #4caf50;
  }
  
  p {
    margin-bottom: 0;
    color: #333;
  }
  
  .small {
    font-size: 12px;
    color: #666;
    margin-top: 8px;
  }
`;

const CloseButton = styled.button`
  margin-top: 20px;
  background-color: #4caf50;
  color: white;
  border: none;
  border-radius: 12px;
  padding: 16px 32px;
  font-size: 18px;
  font-weight: 600;
  cursor: pointer;
  width: 100%;
  transition: all 0.2s;
  box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
  
  &:hover {
    background-color: #43a047;
    transform: translateY(-2px);
    box-shadow: 0 6px 8px rgba(0, 0, 0, 0.15);
  }
  
  &:active {
    transform: translateY(0);
    box-shadow: 0 2px 4px rgba(0, 0, 0, 0.1);
  }
  
  &:disabled {
    background-color: #8bc34a;
    cursor: not-allowed;
    opacity: 0.7;
    transform: none;
  }
`;

const CloseFailureMessage = styled.div`
  margin-top: 10px;
  font-size: 14px;
  color: #dc2626;
  font-style: italic;
  text-align: center;
  padding: 8px;
  border-radius: 6px;
  background-color: #fff1f0;
  border: 1px solid #fecaca;
`;

const PurchaseSummaryInline = styled.div`
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid ${props => props.$valid ? '#bae6fd' : '#fecaca'};
  font-size: 14px;
  color: #4b5563;
  text-align: center;
  
  strong {
    color: #0369a1;
  }
  
  @media (max-width: 768px) {
    font-size: 13px;
    margin-top: 6px;
    padding-top: 6px;
  }
`;

export default App; 

